package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	gh "github.com/shitamachi/forgelet/internal/provider/github"
	"github.com/shitamachi/forgelet/internal/run"
)

type resolvingProvider struct {
	*fakeProvider
	resolveErr error
	calls      int
}

func (p *resolvingProvider) ResolvePullRequest(_ context.Context, _ run.Event) (string, error) {
	p.calls++
	return testSHA, p.resolveErr
}

func TestWebhookPendingMergeRetriesAndPinsBeforeAdmission(t *testing.T) {
	for _, superseded := range []bool{false, true} {
		t.Run(map[bool]string{false: "ready after retry", true: "head moved"}[superseded], func(t *testing.T) {
			h := newHarness(t, 2)
			h.provider.files[".github/workflows/ci.yml"] = []byte(strings.Replace(testWorkflow, "on: push", "on: pull_request", 1))
			provider := &resolvingProvider{fakeProvider: h.provider, resolveErr: errors.New("merge not ready")}
			h.server.provider = provider
			clock := time.Now()
			h.server.now = func() time.Time { return clock }
			body, _ := json.Marshal(map[string]any{"action": "opened", "number": 7, "repository": map[string]string{"full_name": "me/repo"}, "installation": map[string]int{"id": 42}, "pull_request": map[string]any{"merge_commit_sha": nil, "head": map[string]any{"sha": strings.Repeat("b", 40), "repo": map[string]string{"full_name": "me/repo"}}, "base": map[string]any{"sha": strings.Repeat("c", 40), "ref": "main", "repo": map[string]string{"full_name": "me/repo"}}}})
			mac := hmac.New(sha256.New, []byte(h.cfg.WebhookSecret))
			_, _ = mac.Write(body)
			send := func() {
				code, raw := h.request("POST", "/webhooks/github", string(body), map[string]string{"X-GitHub-Event": "pull_request", "X-GitHub-Delivery": "pending-pr", "X-Hub-Signature-256": "sha256=" + hex.EncodeToString(mac.Sum(nil))})
				if code != 202 {
					t.Fatal(code, string(raw))
				}
			}
			send()
			h.tick()
			if runs, err := h.store.ListRuns(t.Context()); err != nil || len(runs) != 0 {
				t.Fatal(runs, err)
			}
			clock = clock.Add(time.Hour)
			if superseded {
				provider.resolveErr = gh.ErrPullRequestSuperseded
			} else {
				provider.resolveErr = nil
			}
			h.tick()
			rows, err := h.store.Events(t.Context())
			if err != nil || len(rows) != 1 {
				t.Fatal(rows, err)
			}
			runs, err := h.store.ListRuns(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if superseded {
				if len(runs) != 0 || rows[0].Error == "" {
					t.Fatal(runs, rows)
				}
				return
			}
			if len(runs) != 1 || runs[0].SHA != testSHA || rows[0].SHA != testSHA {
				t.Fatal(runs, rows)
			}
			send()
			h.tick()
			after, err := h.store.ListRuns(t.Context())
			if err != nil || len(after) != 1 || provider.calls != 2 {
				t.Fatal(after, provider.calls, err)
			}
		})
	}
}
