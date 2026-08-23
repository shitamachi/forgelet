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
