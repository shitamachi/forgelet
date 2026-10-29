package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/executor"
	gh "github.com/shitamachi/forgelet/internal/provider/github"
	"github.com/shitamachi/forgelet/internal/run"
)

func signedLifecycle(h *harness, delivery, event, body string, valid bool) (int, []byte) {
	mac := hmac.New(sha256.New, []byte(h.cfg.WebhookSecret))
	_, _ = mac.Write([]byte(body))
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !valid {
		signature = "invalid"
	}
	return h.request(http.MethodPost, "/webhooks/github", body, map[string]string{"X-Hub-Signature-256": signature, "X-GitHub-Delivery": delivery, "X-GitHub-Event": event})
}
func TestLifecycleSignatureDurableReplayConflictAndFailedRefresh(t *testing.T) {
	h, p := onboardingHarness(t)
	if _, err := h.server.refreshGitHubAuthorization(t.Context()); err != nil {
		t.Fatal(err)
	}
	body := `{"action":"removed","installation":{"id":42},"repositories_removed":[{"id":123}]}`
	if code, _ := signedLifecycle(h, "remove-one", "installation_repositories", body, false); code != 401 {
		t.Fatal(code)
	}
	before := h.server.authorization.Load().Version
	if code, b := signedLifecycle(h, "remove-one", "installation_repositories", body, true); code != 202 {
		t.Fatalf("%d %s", code, b)
	}
	if h.server.authorization.Load().Version != before+1 || h.server.repositoryAccess("me/repo", 42).State != "pending" {
		t.Fatal("hint did not quarantine")
	}
	if code, _ := signedLifecycle(h, "remove-one", "installation_repositories", body, true); code != 202 || h.server.authorization.Load().Version != before+1 {
		t.Fatal("replay mutated snapshot", code)
	}
	if code, _ := signedLifecycle(h, "remove-one", "installation_repositories", `{"action":"added","installation":{"id":42}}`, true); code != 409 {
		t.Fatal(code)
	}
	p.err = errors.New("incomplete second page")
	if _, err := h.server.refreshGitHubAuthorization(t.Context()); err == nil {
		t.Fatal("failure ignored")
	}
	if state := h.server.repositoryAccess("me/repo", 42).State; state != "pending" {
		t.Fatal(state)
	}
	if err := h.server.loadGitHubAuthorization(t.Context()); err != nil {
		t.Fatal(err)
	}
	if code, _ := signedLifecycle(h, "remove-one", "installation_repositories", body, true); code != 202 {
		t.Fatal("replay lost on restart", code)
	}
	p.err = nil
	p.catalog.Repositories = nil
	if _, err := h.server.refreshGitHubAuthorization(t.Context()); err != nil {
		t.Fatal(err)
	}
	if state := h.server.repositoryAccess("me/repo", 42).State; state != "not_authorized" {
		t.Fatal(state)
	}
	p.catalog.Repositories = []gh.Repository{{ID: 123, Name: "me/repo", InstallationID: 42}}
	if _, err := h.server.refreshGitHubAuthorization(t.Context()); err != nil {
		t.Fatal(err)
	}
	if state := h.server.repositoryAccess("me/repo", 0).State; state != "authorized" {
		t.Fatal("zero event installation cannot bypass or prevent matching legacy binding", state)
	}
	if state := h.server.repositoryAccess("me/repo", 99).State; state != "not_authorized" {
		t.Fatal("wrong installation accepted", state)
	}
}

func TestAuthorizationQuarantineKeepsQueuedAndRevocationCancelsActive(t *testing.T) {
	h, p := onboardingHarness(t)
	if _, err := h.server.refreshGitHubAuthorization(t.Context()); err != nil {
		t.Fatal(err)
	}
	first := h.manual(testWorkflow)
	second := h.manual(testWorkflow)
	if _, err := h.server.mutateAuthorization(t.Context(), func(s *GitHubAuthorization) error { s.Quarantined = []int64{42}; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := h.server.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if h.runtime.created != 0 {
		t.Fatal("quarantined queued work started")
	}
	for _, id := range []string{first.ID, second.ID} {
		r, _ := h.store.GetRun(t.Context(), id)
		if r.State != run.Queued {
			t.Fatal("uncertain hint cancelled history", r.State)
		}
	}
	if _, err := h.server.refreshGitHubAuthorization(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := h.server.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	r, _ := h.store.GetRun(t.Context(), first.ID)
	job := r.Jobs[0]
	if code, b := h.runtimeRequest("POST", "claim", job, nil); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	p.catalog.Repositories = nil
	if _, err := h.server.refreshGitHubAuthorization(t.Context()); err != nil {
		t.Fatal(err)
	}
	if code, _ := h.runtimeRequest("GET", "credentials", job, nil); code != 403 {
		t.Fatal("revoked credential allowed", code)
	}
	if err := h.server.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	j, _ := h.store.GetJob(t.Context(), job.ID)
	if !j.CancelRequested || j.CancelReason == "" || j.Result != nil {
		t.Fatal("active stop fabricated result", j)
	}
	if len(h.runtime.deleted) == 0 {
		t.Fatal("normal pod cancellation not called")
	}
	history, _ := h.store.ListRuns(t.Context())
	if len(history) != 2 {
		t.Fatal("history removed")
	}
}

type revokingTokenProvider struct {
	*onboardingFake
	revoke func()
}

func (p *revokingTokenProvider) RepositoryToken(context.Context, int64, string) (string, error) {
	p.revoke()
	return "must-never-be-returned", nil
}
func TestAuthorizationRecheckedAfterTokenMint(t *testing.T) {
	h, p := onboardingHarness(t)
	if _, err := h.server.refreshGitHubAuthorization(t.Context()); err != nil {
		t.Fatal(err)
	}
	e := run.Event{Name: "push", DeliveryID: "token-race", Repository: "me/repo", InstallationID: 42, SHA: testSHA, Ref: "refs/heads/main", Payload: json.RawMessage(`{}`)}
	source := []byte("on: push\njobs:\n  test:\n    runs-on: small\n    steps:\n      - uses: actions/checkout@v4\n")
	item, err := h.server.createRun(t.Context(), e, ".github/workflows/ci.yml", source)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.server.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	item, _ = h.store.GetRun(t.Context(), item.ID)
	job := item.Jobs[0]
	if code, b := h.runtimeRequest("POST", "claim", job, nil); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	h.server.provider = &revokingTokenProvider{onboardingFake: p, revoke: func() {
		_, err := h.server.mutateAuthorization(t.Context(), func(s *GitHubAuthorization) error { s.Quarantined = []int64{42}; return nil })
		if err != nil {
			t.Fatal(err)
		}
	}}
	if code, b := h.runtimeRequest("GET", "credentials", job, nil); code != 503 {
		t.Fatalf("minted token leaked: %d %s", code, b)
	}
}

// A refresh or startup quarantine is temporary uncertainty, not a permanent
// denial. The real remote protocol must wait without releasing secrets or
// executing twice when it races claim/credential acquisition.
func TestRemoteWaitsForAuthorizationConfirmation(t *testing.T) {
	for _, boundary := range []string{"claim", "credentials"} {
		t.Run(boundary, func(t *testing.T) {
			h, _ := onboardingHarness(t)
			h.server.cfg.SecretKey = bytes.Repeat([]byte{7}, 32)
			if _, err := h.server.refreshGitHubAuthorization(t.Context()); err != nil {
				t.Fatal(err)
			}
			if code, body := h.admin("PUT", "/api/repositories/me/repo/secrets/TOKEN", map[string]string{"value": "test-private-value"}); code != 200 {
				t.Fatalf("%d %s", code, body)
			}
			item := h.manual(`on: push
jobs:
  test:
    runs-on: small
    env:
      TOKEN: ${{ secrets.TOKEN }}
    steps:
      - run: test -n "$TOKEN"; echo executed-once
`)
			h.tick()
			job, err := h.store.GetJob(t.Context(), item.Jobs[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			quarantine := func(ctx context.Context) error {
				_, err := h.server.mutateAuthorization(ctx, func(s *GitHubAuthorization) error {
					s.Quarantined = []int64{42}
					return nil
				})
				return err
			}
			if boundary == "claim" {
				if err := quarantine(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			pending := make(chan *httptest.ResponseRecorder, 1)
			var triggered atomic.Bool
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if boundary == "credentials" && strings.HasSuffix(r.URL.Path, "/claim") && triggered.CompareAndSwap(false, true) {
					response := httptest.NewRecorder()
					h.server.Handler().ServeHTTP(response, r)
					if err := quarantine(r.Context()); err != nil {
						http.Error(w, "quarantine failed", 500)
						return
					}
					for k, v := range response.Header() {
						w.Header()[k] = v
					}
					w.WriteHeader(response.Code)
					_, _ = w.Write(response.Body.Bytes())
					return
				}
				response := httptest.NewRecorder()
				h.server.Handler().ServeHTTP(response, r)
				if strings.HasSuffix(r.URL.Path, "/"+boundary) && response.Code != 200 {
					select {
					case pending <- response:
					default:
					}
				}
				for k, v := range response.Header() {
					w.Header()[k] = v
				}
				w.WriteHeader(response.Code)
				_, _ = w.Write(response.Body.Bytes())
			}))
			defer api.Close()
			token := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(token, []byte("token-"+job.ID), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- executor.RunRemote(ctx, executor.RemoteOptions{URL: api.URL, JobID: job.ID, PodUID: job.PodUID, TokenFile: token, Workspace: t.TempDir(), RetryTimeout: 5 * time.Second, PollInterval: 10 * time.Millisecond})
			}()
			select {
			case response := <-pending:
				if response.Code != 503 || strings.Contains(response.Body.String(), "test-private-value") {
					t.Fatalf("uncertain authorization leaked credentials or became permanent: %d %s", response.Code, response.Body.String())
				}
			case <-ctx.Done():
				t.Fatal("no pending response")
			}
			current, err := h.store.GetJob(t.Context(), job.ID)
			if err != nil || current.Result != nil || current.CancelRequested {
				t.Fatal("pending confirmation finalized the attempt", current, err)
			}
			logs, err := h.store.Logs(t.Context(), job.ID, 0)
			if err != nil || len(logs) != 0 {
				t.Fatal("executed code before authorization confirmation", logs, err)
			}
			if _, err := h.server.refreshGitHubAuthorization(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			current, err = h.store.GetJob(t.Context(), job.ID)
			if err != nil || current.State != run.Succeeded || current.PodUID != job.PodUID || current.Result == nil || current.Progress == nil || current.Result.LastProgressSeq != current.Progress.Seq {
				t.Fatal("same attempt did not finish with progress ACK", current, err)
			}
			logs, err = h.store.Logs(t.Context(), job.ID, 0)
			data, _ := json.Marshal(logs)
			if err != nil || strings.Count(string(data), "executed-once") != 1 {
				t.Fatal("step was not executed exactly once", string(data), err)
			}
		})
	}
}
