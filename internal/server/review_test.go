package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/executor"
	"github.com/shitamachi/forgelet/internal/kube"
	"github.com/shitamachi/forgelet/internal/run"
)

type failingRuntime struct {
	Runtime
	ensure, observe, deleteError error
}

func (f failingRuntime) Ensure(ctx context.Context, j run.Job, p kube.Profile) (string, error) {
	if f.ensure != nil {
		return "", f.ensure
	}
	return f.Runtime.Ensure(ctx, j, p)
}
func (f failingRuntime) Observe(ctx context.Context, j run.Job) (kube.Observation, error) {
	if f.observe != nil {
		return kube.Observation{}, f.observe
	}
	return f.Runtime.Observe(ctx, j)
}
func (f failingRuntime) Delete(ctx context.Context, j run.Job) error {
	if f.deleteError != nil {
		return f.deleteError
	}
	return f.Runtime.Delete(ctx, j)
}
func TestPermanentRuntimeFailureAndBoundedTransientRetry(t *testing.T) {
	for _, mode := range []string{"ensure", "observe", "transient"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, 1)
			broken := failingRuntime{Runtime: h.runtime}
			switch mode {
			case "transient":
				broken.ensure = errors.New("temporary API outage")
			case "observe":
				broken.observe = fmt.Errorf("%w: ownership mismatch", kube.ErrPermanent)
			default:
				broken.ensure = fmt.Errorf("%w: forbidden", kube.ErrPermanent)
			}
			h.server.runtime = broken
			record := h.manual(testWorkflow)
			_ = h.server.Tick(t.Context())
			job, _ := h.store.GetJob(t.Context(), record.Jobs[0].ID)
			switch mode {
			case "transient":
				if job.State != run.Starting || job.RuntimeFailures != 1 {
					t.Fatal(job)
				}
				_ = h.server.Tick(t.Context())
				job, _ = h.store.GetJob(t.Context(), job.ID)
				if job.RuntimeFailures != 1 {
					t.Fatal("backoff ignored")
				}
				for i := 1; i < 12; i++ {
					at := time.Now().Add(time.Duration(i) * time.Hour)
					h.server.now = func() time.Time { return at }
					_ = h.server.Tick(t.Context())
				}
			case "observe":
				_ = h.server.Tick(t.Context())
			}
			job, _ = h.store.GetJob(t.Context(), job.ID)
			if job.State != run.Interrupted {
				t.Fatal(job)
			}
			h.server.runtime = h.runtime
			next := h.manual(testWorkflow)
			h.tick()
			j, _ := h.store.GetJob(t.Context(), next.Jobs[0].ID)
			if j.State != run.Starting {
				t.Fatal("capacity not released", j.State)
			}
		})
	}
}
func TestRerunRespectsRepositoryDisableAndCurrentProfiles(t *testing.T) {
	for _, mode := range []string{"disabled", "missing-profile", "missing-builder"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, 1)
			source := testWorkflow
			if mode == "missing-builder" {
				profile := h.server.cfg.Profiles["small"]
				profile.Build = &kube.BuildProfile{Images: []string{"ghcr.io/test/app"}}
				h.server.cfg.Profiles["small"] = profile
				source = strings.Replace(testWorkflow, "run: echo hello", "uses: forgelet/build@v1\n        with: {image: ghcr.io/test/app}", 1)
			}
			item := h.manual(source)
			h.tick()
			h.complete(item.Jobs[0])
			switch mode {
			case "disabled":
				doc := h.server.management()
				doc.Repositories[0].Enabled = false
				h.server.managed.Store(&doc)
			case "missing-profile":
				delete(h.server.cfg.Profiles, "small")
			default:
				profile := h.server.cfg.Profiles["small"]
				profile.Build = nil
				h.server.cfg.Profiles["small"] = profile
			}
			status, _ := h.admin(http.MethodPost, "/api/runs/"+item.ID+"/rerun", nil)
			expected := 422
			if mode == "disabled" {
				expected = 403
			}
			if status != expected {
				t.Fatal(status)
			}
			records, _ := h.store.ListRuns(t.Context())
			if len(records) != 1 {
				t.Fatal("rejected rerun created execution")
			}
		})
	}
}
func TestContinueOnErrorInfrastructureFailureThroughRemoteHTTPAndSQLite(t *testing.T) {
	h := newHarness(t, 1)
	source := `on: push
jobs:
  broken:
    runs-on: small
    continue-on-error: true
    steps:
      - id: bad
        run: printf 'bad=notjson\n' >> "$GITHUB_OUTPUT"
      - env: {X: '${{ fromJSON(steps.bad.outputs.bad) }}'}
        run: echo unreachable
`
	item := h.manual(source)
	h.tick()
	job, _ := h.store.GetJob(t.Context(), item.Jobs[0].ID)
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("token-"+job.ID), 0600); err != nil {
		t.Fatal(err)
	}
	err := executor.RunRemote(t.Context(), executor.RemoteOptions{URL: h.http.URL, JobID: job.ID, PodUID: job.PodUID, TokenFile: tokenFile, Workspace: t.TempDir(), Client: h.http.Client(), RetryTimeout: time.Second})
	if err == nil {
		t.Fatal("executor silently succeeded")
	}
	got, _ := h.store.GetRun(t.Context(), item.ID)
	if got.State != run.Failed || got.Jobs[0].Result.Error == "" {
		t.Fatal(got)
	}
}
func TestSameNameNewRepositoryDoesNotInheritOldConfiguration(t *testing.T) {
	h, p := onboardingHarness(t)
	status, body := h.admin(http.MethodPost, "/api/github/enable", onboardingInput{RepositoryID: 123})
	if status != 200 {
		t.Fatal(string(body))
	}
	before := h.server.management()
	p.catalog.Repositories[0].ID = 124
	status, _ = h.admin(http.MethodPost, "/api/github/enable", onboardingInput{RepositoryID: 124, Version: before.Version})
	if status != 409 {
		t.Fatal(status)
	}
	after := h.server.management()
	if after.Version != before.Version || after.Repositories[0].RepositoryID != 123 {
		t.Fatal(after)
	}
}
func TestEmptyPublicURLDoesNotGenerateRelativeCheckURL(t *testing.T) {
	h := newHarness(t, 1)
	h.server.cfg.PublicURL = ""
	if h.server.checkURL("run") != "" {
		t.Fatal(h.server.checkURL("run"))
	}
}
func TestLinkedAdministratorRequiresOriginalEncryptionKey(t *testing.T) {
	h := sessionHarness(t)
	_, challenge := oauthFixture(t, h)
	h.signIn()
	state := h.startOAuth(true, challenge)
	if code, _, _ := h.call("GET", "/api/auth/github/callback?code=valid&state="+state, nil, nil); code != 303 {
		t.Fatal(code)
	}
	path := filepath.Join(h.cfg.DataDir, "auth-encryption.key")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	server, err := New(h.cfg, h.store, h.runtime, h.provider)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.InitAuth(t.Context()); err == nil {
		t.Fatal("missing key regenerated silently")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("lost key was replaced")
	}
}
func TestRetryEndpointsAndLightweightRunSummary(t *testing.T) {
	h := newHarness(t, 1)
	item := h.manual(testWorkflow)
	h.tick()
	code, body := h.admin(http.MethodGet, "/api/runs", nil)
	var records []run.Run
	if code != 200 || json.Unmarshal(body, &records) != nil || len(records[0].Jobs[0].Plan.Steps) != 0 {
		t.Fatal(string(body))
	}
	code, _ = h.admin(http.MethodPost, "/api/runs/"+item.ID+"/checks/retry", map[string]any{})
	if code != 200 {
		t.Fatal(code)
	}
	if code, _ = h.request(http.MethodPost, "/api/runs/"+item.ID+"/checks/retry", "{}", map[string]string{"Content-Type": "application/json"}); code != 401 {
		t.Fatal(code)
	}
	if strings.Contains(string(body), "echo hello") {
		t.Fatal("steps leaked into run list")
	}
}

func TestCancellationDeletionFailuresDoNotResetRetryBudget(t *testing.T) {
	h := newHarness(t, 1)
	record := h.manual(testWorkflow)
	h.tick()
	if err := h.store.CancelRun(t.Context(), record.ID); err != nil {
		t.Fatal(err)
	}
	h.server.runtime = failingRuntime{Runtime: h.runtime, deleteError: errors.New("delete unavailable")}
	for i := range 12 {
		at := time.Now().Add(time.Duration(i) * time.Hour)
		h.server.now = func() time.Time { return at }
		_ = h.server.Tick(t.Context())
	}
	job, err := h.store.GetJob(t.Context(), record.Jobs[0].ID)
	if err != nil || job.State != run.Cancelled || job.RuntimeFailures != 12 {
		t.Fatal(job, err)
	}
	h.server.runtime = h.runtime
	h.tick()
}
func TestOriginalEncryptionKeyCannotBeReplaced(t *testing.T) {
	h := sessionHarness(t)
	_, challenge := oauthFixture(t, h)
	h.signIn()
	state := h.startOAuth(true, challenge)
	if code, _, _ := h.call("GET", "/api/auth/github/callback?code=valid&state="+state, nil, nil); code != 303 {
		t.Fatal(code)
	}
	cfg := h.cfg
	cfg.SecretKey = []byte(strings.Repeat("x", 32))
	server, err := New(cfg, h.store, h.runtime, h.provider)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.InitAuth(t.Context()); err == nil {
		t.Fatal("wrong key accepted")
	}
}
