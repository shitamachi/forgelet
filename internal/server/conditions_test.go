package server_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/server"
	"github.com/shitamachi/forgelet/internal/storage/memory"
)

const conditionalJobsWorkflow = `name: Cond

on:
  push:
    branches:
      - main

jobs:
  build:
    runs-on: k3s-small
    steps:
      - run: echo built

  publish-main-only:
    if: github.ref == 'refs/heads/main'
    runs-on: k3s-small
    steps:
      - run: echo published

  publish-never:
    if: github.event_name == 'pull_request'
    runs-on: k3s-small
    steps:
      - run: echo never
`

// Job-level `if:` is evaluated scheduler-side: a false condition projects
// the instance to skipped (GitHub semantics: skipped jobs do not fail the
// run) without ever dispatching an active object.
func TestJobConditionsSkipAtScheduler(t *testing.T) {
	ctx := context.Background()
	workflows := t.TempDir()
	if err := os.WriteFile(filepath.Join(workflows, "ci.yml"), []byte(conditionalJobsWorkflow), 0o644); err != nil {
		t.Fatal(err)
	}
	durable := memory.NewDurableStore(func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, nil)
	reporter := &capturedReporter{}
	srv, err := server.NewServer(server.Options{
		WebhookSecret: []byte("whsec"),
		WorkflowsDir:  workflows,
		CheckReporter: reporter,
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

	resp := postEvent(t, ctx, ts, "push", "cond-d1", pushPayload)
	if resp.code != 200 || !bytes.Contains([]byte(resp.body), []byte(`"created":true`)) {
		t.Fatalf("webhook: %d %s", resp.code, resp.body)
	}
	run := durable.Runs()[0]
	jobs, err := durable.ListJobRuns(ctx, run.ID)
	if err != nil || len(jobs) != 3 {
		t.Fatalf("jobs=%d err=%v", len(jobs), err)
	}

	statusByKey := map[string]model.JobRunStatus{}
	for _, j := range jobs {
		statusByKey[j.JobKey] = j.Status
	}
	if statusByKey["build"] != model.JobQueued {
		t.Errorf("build = %s, want queued", statusByKey["build"])
	}
	if statusByKey["publish-never"] != model.JobSkipped {
		t.Errorf("publish-never = %s, want skipped", statusByKey["publish-never"])
	}
	if got := statusByKey["publish-main-only"]; got != model.JobQueued {
		t.Errorf("publish-main-only = %s, want queued on main push", got)
	}
	if c, ok := reporter.latestByExternal(string(jobIDByKey(t, jobs, "publish-never"))); !ok ||
		c.Conclusion != "skipped" {
		t.Errorf("skipped check = %+v", c)
	}

	// Only the two live jobs are dispatchable.
	n, err := srv.DispatchOnce(ctx)
	if err != nil || n != 2 {
		t.Fatalf("dispatch: n=%d err=%v", n, err)
	}
	finalRun, err := durable.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Queued-but-undispatched jobs keep the run open; nothing failed.
	if finalRun.Status == model.RunFailed {
		t.Errorf("run = %s, want non-failed aggregate", finalRun.Status)
	}
}

func TestJobConditionsDeferredNeeds(t *testing.T) {
	// Workflow where deploy runs only when test fails (failure() + needs).
	// The condition must be deferred to dispatch time, not rejected at ingest.
	const workflow = `name: Cond
on: push
jobs:
  test:
    runs-on: small
    steps:
      - run: exit 1
  deploy-failure:
    needs: test
    if: failure()
    runs-on: small
    steps:
      - run: echo deploy
  deploy-success:
    needs: test
    if: success()
    runs-on: small
    steps:
      - run: echo deploy-success
`
	ctx := context.Background()
	workflows := t.TempDir()
	if err := os.WriteFile(filepath.Join(workflows, "ci.yml"), []byte(workflow), 0o644); err != nil {
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

	resp := postEvent(t, ctx, ts, "push", "deferred-d1", pushPayload)
	if resp.code != 200 || !bytes.Contains([]byte(resp.body), []byte(`"created":true`)) {
		t.Fatalf("webhook: %d %s", resp.code, resp.body)
	}
	run := durable.Runs()[0]
	jobs, _ := durable.ListJobRuns(ctx, run.ID)
	if len(jobs) != 3 {
		t.Fatalf("jobs=%d", len(jobs))
	}
	// All three are queued initially; deferred conditions are not evaluated at build time.
	for _, j := range jobs {
		if j.Status != model.JobQueued {
			t.Errorf("initial %s = %s, want queued", j.JobKey, j.Status)
		}
	}
	// Dispatch test (only test is ready)
	n, err := srv.DispatchOnce(ctx)
	if err != nil || n != 1 {
		t.Fatalf("dispatch test: n=%d err=%v", n, err)
	}
	testID := jobIDByKey(t, jobs, "test")
	// Mark test as failed (as if pod failed)
	if err := durable.ApplyObserved(ctx, testID, model.PhaseFailed, time.Unix(1_700_000_100, 0).UTC()); err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	// Next dispatch should evaluate deferred conditions: deploy-failure should run, deploy-success should be skipped.
	n, err = srv.DispatchOnce(ctx)
	if err != nil {
		t.Fatalf("dispatch deferred: %v", err)
	}
	if n != 1 {
		t.Fatalf("dispatch deferred: n=%d, want 1 (only failure branch)", n)
	}
	jobs, _ = durable.ListJobRuns(ctx, run.ID)
	statusByKey := map[string]model.JobRunStatus{}
	for _, j := range jobs {
		statusByKey[j.JobKey] = j.Status
	}
	if statusByKey["deploy-failure"] != model.JobDispatched && statusByKey["deploy-failure"] != model.JobRunning {
		// Dispatched jobs are at least dispatched
		t.Errorf("deploy-failure = %s, want dispatched/running", statusByKey["deploy-failure"])
	}
	if statusByKey["deploy-success"] != model.JobSkipped {
		t.Errorf("deploy-success = %s, want skipped", statusByKey["deploy-success"])
	}
	// Explicit needs equality should also be deferred and work.
	const workflow2 = `name: Cond2
on: push
jobs:
  test:
    runs-on: small
    steps:
      - run: exit 1
  explicit:
    needs: test
    if: needs.test.result == 'failure'
    runs-on: small
    steps:
      - run: echo explicit
`
	workflows2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(workflows2, "ci.yml"), []byte(workflow2), 0o644); err != nil {
		t.Fatalf("write workflow2: %v", err)
	}
	durable2 := memory.NewDurableStore(func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, nil)
	srv2, _ := server.NewServer(server.Options{
		WebhookSecret: []byte("whsec"), WorkflowsDir: workflows2,
		CheckReporter: &capturedReporter{}, Durable: durable2,
		TokenKey: bytes.Repeat([]byte{0x42}, 32),
		Now:      func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		Log:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	ts2 := httptest.NewServer(srv2.Handler())
	defer ts2.Close()
	resp = postEvent(t, ctx, ts2, "push", "deferred-d2", pushPayload)
	if resp.code != 200 {
		t.Fatalf("webhook2: %d %s", resp.code, resp.body)
	}
	run2 := durable2.Runs()[0]
	jobs2, _ := durable2.ListJobRuns(ctx, run2.ID)
	// Dispatch test
	if _, err := srv2.DispatchOnce(ctx); err != nil {
		t.Fatalf("dispatch test2: %v", err)
	}
	testID2 := jobIDByKey(t, jobs2, "test")
	if err := durable2.ApplyObserved(ctx, testID2, model.PhaseFailed, time.Unix(1_700_000_100, 0).UTC()); err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	n, _ = srv2.DispatchOnce(ctx)
	if n != 1 {
		t.Fatalf("explicit needs dispatch: n=%d, want 1", n)
	}
	jobs2, _ = durable2.ListJobRuns(ctx, run2.ID)
	for _, j := range jobs2 {
		if j.JobKey == "explicit" && j.Status != model.JobDispatched {
			t.Errorf("explicit = %s, want dispatched", j.Status)
		}
	}
}

func jobIDByKey(t *testing.T, jobs []model.JobRunRecord, key string) model.JobRunID {
	t.Helper()
	for _, j := range jobs {
		if j.JobKey == key {
			return j.ID
		}
	}
	t.Fatalf("job %q not found", key)
	return ""
}
