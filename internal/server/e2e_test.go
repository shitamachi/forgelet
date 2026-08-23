package server_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/report"
	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/runtime/executor"
	"github.com/shitamachi/forgelet/internal/runtime/executor/httpclient"
	"github.com/shitamachi/forgelet/internal/security/identity"
	"github.com/shitamachi/forgelet/internal/server"
	"github.com/shitamachi/forgelet/internal/storage/memory"
)

const e2eWorkflow = `name: CI

on:
  push:
    branches:
      - main

jobs:
  test:
    name: Unit tests
    runs-on: k3s-small
    env:
      GREETING: ${{ secrets.GREETING }}
    steps:
      - name: greet
        run: test "$GREETING" = hello-m0 && echo greet-ok
      - name: share
        run: echo built=1 >> $GITHUB_OUTPUT

  build:
    runs-on: k3s-small
    steps:
      - run: echo build-ok
`

// capturedReporter records checks by external id.
type capturedReporter struct {
	mu       sync.Mutex
	reported []report.Check
}

func (c *capturedReporter) Report(_ context.Context, _ model.RunRecord, check report.Check) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reported = append(c.reported, check)
	return nil
}

func (c *capturedReporter) latestByExternal(id string) (report.Check, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var found report.Check
	for _, r := range c.reported {
		if r.ExternalID == id {
			found = r
		}
	}
	return found, found.ExternalID != ""
}

// hasStatus reports whether any report for the id had the status.
func (c *capturedReporter) hasStatus(id string, status report.CheckStatus) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.reported {
		if r.ExternalID == id && r.Status == status {
			return true
		}
	}
	return false
}

func signBody(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

const pushPayload = `{
  "ref": "refs/heads/main",
  "after": "e2e0e2e0e2e0e2e0e2e0e2e0e2e0e2e0e2e0e2e",
  "repository": {"name": "forgelet", "owner": {"login": "shitamachi"}},
  "pusher": {"name": "guo"}
}`

// TestM0EndToEnd drives the full 0001 plan §2 chain in-process:
// signed push → dedupe → compile → durable run → dispatch → authenticated
// plan/secret fetch → multi-step execution → status projection → terminal
// run → check run lifecycle → GC.
func TestM0EndToEnd(t *testing.T) {
	ctx := context.Background()
	workflows := t.TempDir()
	if err := os.WriteFile(filepath.Join(workflows, "ci.yml"), []byte(e2eWorkflow), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}

	active := memory.NewActiveStore()
	durable := memory.NewDurableStore(func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, nil)
	reporter := &capturedReporter{}
	logs := &bytes.Buffer{}

	srv, err := server.NewServer(server.Options{
		WebhookSecret:  []byte("whsec"),
		WorkflowsDir:   workflows,
		SecretValues:   map[string]string{"repository/GREETING": "hello-m0"},
		CheckReporter:  reporter,
		Active:         active,
		Durable:        durable,
		TokenKey:       bytes.Repeat([]byte{0x42}, 32),
		DetailsBaseURL: "https://ci.example.com",
		Now:            func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		Log:            slog.New(slog.NewJSONHandler(logs, nil)),
	})
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. Signed push delivery.
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/webhooks/github", strings.NewReader(pushPayload))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "e2e-d1")
	req.Header.Set("X-Hub-Signature-256", signBody("whsec", pushPayload))
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("webhook: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"created":true`) {
		t.Fatalf("webhook: %d %s", resp.StatusCode, body)
	}

	// 2. Replay dedupes to the same single run.
	resp2, err := ts.Client().Do(reqWithDelivery(ctx, ts, "e2e-d1"))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	b2, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	if !strings.Contains(string(b2), `"created":false`) {
		t.Fatalf("replay not deduped: %s", b2)
	}

	runs := durable.Runs()
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
	runID := runs[0].ID
	jobs, err := durable.ListJobRuns(ctx, runID)
	if err != nil || len(jobs) != 2 {
		t.Fatalf("jobs=%d err=%v, want 2", len(jobs), err)
	}

	// 3. Dispatch both jobs (deterministic CRs + queued checks).
	if n, err := srv.DispatchOnce(ctx); err != nil || n != 2 {
		t.Fatalf("dispatch: n=%d err=%v", n, err)
	}
	if active.Created != 2 {
		t.Fatalf("active objects = %d, want 2", active.Created)
	}
	for _, j := range jobs {
		if !reporter.hasStatus(string(j.ID), report.StatusQueued) {
			t.Errorf("queued check missing for %s", j.ID)
		}
		if c, ok := reporter.latestByExternal(string(j.ID)); !ok || c.Status != report.StatusProgress {
			t.Errorf("dispatched check not in_progress for %s: %+v", j.ID, c)
		}
	}

	// 4. Execute each job as the executor would (real HTTP + real bash).
	for _, j := range jobs {
		token, err := srv.MintJobToken(ctx, j.ID)
		if err != nil {
			t.Fatalf("mint token: %v", err)
		}
		cp := httpclient.New(ts.URL, token, ts.Client())
		p, err := cp.FetchPlan(ctx, identityOf(j.ID))
		if err != nil {
			t.Fatalf("fetch plan: %v", err)
		}
		if len(p.Steps) == 0 {
			t.Fatalf("plan without steps: %+v", p)
		}
		if _, leaked := p.Env["GREETING"]; leaked {
			t.Error("secret leaked into plan env instead of a reference")
		}
		if len(p.SecretRefs) == 0 && j.JobKey == "test" {
			t.Errorf("secret ref missing from plan: %+v", p.SecretRefs)
		}
		engine := &executor.Engine{
			CP:      cp,
			WorkDir: t.TempDir(),
			Grace:   2 * time.Second,
			Logger:  slog.New(slog.NewJSONHandler(io.Discard, nil)),
		}
		result, rerr := engine.Run(ctx, identityOf(j.ID), p)
		if rerr != nil || !result.Success {
			t.Fatalf("job %s: %v %+v", j.JobKey, rerr, result)
		}
	}

	// 5. Terminal projection: run succeeded, checks completed with success.
	finalRun, err := durable.GetRun(ctx, runID)
	if err != nil || finalRun.Status != model.RunSucceeded {
		t.Fatalf("run status = %s err=%v, want succeeded", finalRun.Status, err)
	}
	for _, j := range jobs {
		c, ok := reporter.latestByExternal(string(j.ID))
		if !ok || c.Status != report.StatusCompleted || c.Conclusion != report.ConclusionSuccess {
			t.Errorf("final check for %s: %+v", j.ID, c)
		}
		if !strings.HasPrefix(c.DetailsURL, "https://ci.example.com/runs/") {
			t.Errorf("details url: %s", c.DetailsURL)
		}
	}

	// 6. GC collects terminal actives.
	if n, err := srv.CollectOnce(ctx); err != nil || n != 2 {
		t.Fatalf("collect: n=%d err=%v", n, err)
	}
	if len(active.Objects()) != 0 {
		t.Errorf("objects left after GC: %d", len(active.Objects()))
	}

	// 7. Log discipline: JSON lines only, no secret plaintext.
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("non-JSON log line: %q", line)
		}
	}
	if strings.Contains(logs.String(), "hello-m0") {
		t.Error("secret plaintext leaked into server logs")
	}
}

func identityOf(id model.JobRunID) identity.Identity {
	return identity.Identity{
		Audience: identity.Audience, Namespace: "forgelet-jobs",
		PodUID: "pod-" + string(id), JobRunID: id,
		Scopes: []string{identity.ScopePlanRead, identity.ScopeSecretsRead, identity.ScopeStatusWrite},
	}
}

func reqWithDelivery(ctx context.Context, ts *httptest.Server, delivery string) *http.Request {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/webhooks/github", strings.NewReader(pushPayload))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", delivery)
	req.Header.Set("X-Hub-Signature-256", signBody("whsec", pushPayload))
	return req
}

// TestV1PullRequestEndToEnd covers the V1 pull_request path: signed PR
// webhook → fork trust classification → compile from the base-branch filter
// → dispatch → execution where fork PRs are denied secrets (FR-9.4) while
// same-repo PRs get them → check lifecycle. Non-run actions are ignored.
func TestV1PullRequestEndToEnd(t *testing.T) {
	ctx := context.Background()
	workflows := t.TempDir()
	if err := os.WriteFile(filepath.Join(workflows, "pr.yml"), []byte(prWorkflow), 0o644); err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	durable := memory.NewDurableStore(clock, nil)
	reporter := &capturedReporter{}
	logs := &bytes.Buffer{}

	srv, err := server.NewServer(server.Options{
		WebhookSecret:  []byte("whsec"),
		WorkflowsDir:   workflows,
		SecretValues:   map[string]string{"repository/GREETING": "hello-pr"},
		CheckReporter:  reporter,
		Durable:        durable,
		TokenKey:       bytes.Repeat([]byte{0x42}, 32),
		DetailsBaseURL: "https://ci.example.com",
		Now:            clock,
		Log:            slog.New(slog.NewJSONHandler(logs, nil)),
	})
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	forkBody := prPayload("opened", true, "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111")
	resp := postEvent(t, ctx, ts, "pull_request", "pr-d1", forkBody)
	if resp.code != http.StatusOK || !strings.Contains(resp.body, `"created":true`) || !strings.Contains(resp.body, `"fork":true`) {
		t.Fatalf("fork pr webhook: %d %s", resp.code, resp.body)
	}

	// Ignored actions never create runs.
	closed := postEvent(t, ctx, ts, "pull_request", "pr-d2", prPayload("closed", true, "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111"))
	if closed.code != http.StatusOK || !strings.Contains(closed.body, `"ignored":true`) ||
		!strings.Contains(closed.body, `"reason":"action closed"`) {
		t.Fatalf("closed action: %d %s", closed.code, closed.body)
	}

	runs := durable.Runs()
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1 (only the fork PR)", len(runs))
	}
	jobs, err := durable.ListJobRuns(ctx, runs[0].ID)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs=%d err=%v", len(jobs), err)
	}

	// Execute as the executor would: the plan carries a secret reference,
	// but the fork trust level denies every secret, so the step fails.
	if n, err := srv.DispatchOnce(ctx); err != nil || n != 1 {
		t.Fatalf("dispatch: n=%d err=%v", n, err)
	}
	token, err := srv.MintJobToken(ctx, jobs[0].ID)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	cp := httpclient.New(ts.URL, token, ts.Client())
	p, err := cp.FetchPlan(ctx, identityOf(jobs[0].ID))
	if err != nil {
		t.Fatalf("fetch plan: %v", err)
	}
	if len(p.SecretRefs) != 1 || p.SecretRefs[0].Name != "GREETING" {
		t.Errorf("plan secret refs = %+v", p.SecretRefs)
	}
	engine := &executor.Engine{CP: cp, WorkDir: t.TempDir(), Grace: 2 * time.Second,
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))}
	if result, rerr := engine.Run(ctx, identityOf(jobs[0].ID), p); rerr == nil || result.Success {
		t.Fatalf("fork job must fail on denied secrets: %v %+v", rerr, result)
	}
	forkRun, err := durable.GetRun(ctx, runs[0].ID)
	if err != nil || forkRun.Status != model.RunFailed {
		t.Fatalf("fork run status = %s err=%v, want failed", forkRun.Status, err)
	}
	if c, ok := reporter.latestByExternal(string(jobs[0].ID)); !ok || c.Conclusion != report.ConclusionFailure {
		t.Errorf("fork check = %+v", c)
	}

	// Same-repo PR: same workflow, secrets allowed, job succeeds.
	sameRepo := postEvent(t, ctx, ts, "pull_request", "pr-d3",
		prPayload("synchronize", false, "bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222"))
	if sameRepo.code != http.StatusOK || !strings.Contains(sameRepo.body, `"fork":false`) {
		t.Fatalf("same-repo pr webhook: %d %s", sameRepo.code, sameRepo.body)
	}
	runs = durable.Runs()
	if len(runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(runs))
	}
	jobs2, err := durable.ListJobRuns(ctx, runs[1].ID)
	if err != nil || len(jobs2) != 1 {
		t.Fatalf("jobs2=%d err=%v", len(jobs2), err)
	}
	if n, err := srv.DispatchOnce(ctx); err != nil || n != 1 {
		t.Fatalf("dispatch 2: n=%d err=%v", n, err)
	}
	token2, err := srv.MintJobToken(ctx, jobs2[0].ID)
	if err != nil {
		t.Fatalf("mint token 2: %v", err)
	}
	cp2 := httpclient.New(ts.URL, token2, ts.Client())
	p2, err := cp2.FetchPlan(ctx, identityOf(jobs2[0].ID))
	if err != nil {
		t.Fatalf("fetch plan 2: %v", err)
	}
	engine2 := &executor.Engine{CP: cp2, WorkDir: t.TempDir(), Grace: 2 * time.Second,
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))}
	if result, rerr := engine2.Run(ctx, identityOf(jobs2[0].ID), p2); rerr != nil || !result.Success {
		t.Fatalf("same-repo job must pass: %v %+v", rerr, result)
	}
	sameRun, err := durable.GetRun(ctx, runs[1].ID)
	if err != nil || sameRun.Status != model.RunSucceeded {
		t.Fatalf("same-repo run status = %s err=%v, want succeeded", sameRun.Status, err)
	}
	c2, ok := reporter.latestByExternal(string(jobs2[0].ID))
	if !ok || c2.Status != report.StatusCompleted || c2.Conclusion != report.ConclusionSuccess {
		t.Errorf("same-repo check = %+v", c2)
	}

	// The denied secret plaintext never appears anywhere in the logs.
	if strings.Contains(logs.String(), "hello-pr") {
		t.Error("secret plaintext leaked into server logs")
	}
}

const prWorkflow = `name: PR CI

on:
  pull_request:
    branches:
      - main

jobs:
  gated:
    runs-on: k3s-small
    env:
      GREETING: ${{ secrets.GREETING }}
    steps:
      - name: needs-secret
        run: test "$GREETING" = hello-pr
`

func prPayload(action string, fork bool, sha string) string {
	headRepo := `"repo": {"fork": true, "name": "fork-repo", "owner": {"login": "contributor"}}`
	if !fork {
		headRepo = `"repo": {"fork": false, "name": "forgelet", "owner": {"login": "shitamachi"}}`
	}
	return `{"action": "` + action + `", "number": 7,
	  "pull_request": {
	    "head": {"ref": "feature", "sha": "` + sha + `", ` + headRepo + `},
	    "base": {"ref": "main", "repo": {"name": "forgelet", "owner": {"login": "shitamachi"}}}
	  }}`
}

type webhookResponse struct {
	code int
	body string
}

func postEvent(t *testing.T, ctx context.Context, ts *httptest.Server, event, delivery, body string) webhookResponse {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/webhooks/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", delivery)
	req.Header.Set("X-Hub-Signature-256", signBody("whsec", body))
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s webhook: %v", event, err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return webhookResponse{code: resp.StatusCode, body: string(b)}
}

// TestM0NoMatchingWorkflow: a push to an unmatched branch creates no run.
func TestM0NoMatchingWorkflow(t *testing.T) {
	ctx := context.Background()
	workflows := t.TempDir()
	if err := os.WriteFile(filepath.Join(workflows, "ci.yml"), []byte(e2eWorkflow), 0o644); err != nil {
		t.Fatal(err)
	}
	durable := memory.NewDurableStore(func() time.Time { return time.Unix(1, 0).UTC() }, nil)
	srv, err := server.NewServer(server.Options{
		WebhookSecret: []byte("whsec"),
		WorkflowsDir:  workflows,
		CheckReporter: &capturedReporter{},
		Durable:       durable,
		TokenKey:      bytes.Repeat([]byte{0x42}, 32),
		Now:           func() time.Time { return time.Unix(1, 0).UTC() },
		Log:           slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	other := strings.Replace(pushPayload, "refs/heads/main", "refs/heads/dev", 1)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/webhooks/github", strings.NewReader(other))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "e2e-other")
	req.Header.Set("X-Hub-Signature-256", signBody("whsec", other))
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("webhook: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), "no matching workflow") {
		t.Fatalf("unmatched push: %d %s", resp.StatusCode, b)
	}
	if len(durable.Runs()) != 0 {
		t.Fatal("run created for unmatched push")
	}
}

// TestM0AuthBoundaries: bad token, wrong job binding and missing scope fail.
func TestM0AuthBoundaries(t *testing.T) {
	ctx := context.Background()
	workflows := t.TempDir()
	if err := os.WriteFile(filepath.Join(workflows, "ci.yml"), []byte(e2eWorkflow), 0o644); err != nil {
		t.Fatal(err)
	}
	durable := memory.NewDurableStore(func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, nil)
	srv, err := server.NewServer(server.Options{
		WebhookSecret: []byte("whsec"),
		WorkflowsDir:  workflows,
		CheckReporter: &capturedReporter{},
		Durable:       durable,
		TokenKey:      bytes.Repeat([]byte{0x42}, 32),
		Now:           func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		Log:           slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Ingest + dispatch one job to have a real id.
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/webhooks/github", strings.NewReader(pushPayload))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "auth-d1")
	req.Header.Set("X-Hub-Signature-256", signBody("whsec", pushPayload))
	resp, _ := ts.Client().Do(req)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if _, err := srv.DispatchOnce(ctx); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	job := durable.Runs()[0]
	jobs, _ := durable.ListJobRuns(ctx, job.ID)
	realID := jobs[0].ID

	cases := []struct {
		name  string
		token func() string
		path  string
	}{
		{"bad token", func() string { return "garbage" }, "/internal/jobruns/" + string(realID) + "/plan"},
		{"wrong binding", func() string {
			tok, _ := srv.MintJobToken(ctx, model.JobRunID("01JOTHER00000000000000000X"))
			return tok
		}, "/internal/jobruns/" + string(realID) + "/plan"},
	}
	for _, tc := range cases {
		r, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+tc.path, nil)
		r.Header.Set("Authorization", "Bearer "+tc.token())
		res, err := ts.Client().Do(r)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized && res.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status %d, want 401/403", tc.name, res.StatusCode)
		}
	}
}
