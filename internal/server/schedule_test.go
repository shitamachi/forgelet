package server_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/server"
	"github.com/shitamachi/forgelet/internal/storage/memory"
)

const scheduleWorkflow = `name: Nightly

on:
  schedule:
    - cron: "*/5 * * * *"

jobs:
  nightly:
    runs-on: k3s-small
    steps:
      - run: echo nightly-ok
`

const pushOnlyWorkflow = `name: Push CI

on:
  push:
    branches:
      - main

jobs:
  pushjob:
    runs-on: k3s-small
    steps:
      - run: echo push-ok
`

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time { return c.t }

// fakeGHSource is a repository workflow source that also resolves default
// branches, mirroring the GitHub content adapter.
type fakeGHSource struct {
	files  []server.WorkflowFile
	gotRef string
}

func (f *fakeGHSource) FetchWorkflows(_ context.Context, _ model.RepositoryRef, ref string) ([]server.WorkflowFile, error) {
	f.gotRef = ref
	return f.files, nil
}

func (f *fakeGHSource) DefaultBranch(context.Context, model.RepositoryRef) (string, error) {
	return "trunk", nil
}

func newScheduleTestServer(t *testing.T, clock *fakeClock, opts server.Options) (*server.Server, *memory.DurableStore) {
	t.Helper()
	durable := memory.NewDurableStore(clock.Now, nil)
	opts.CheckReporter = &capturedReporter{}
	opts.Durable = durable
	opts.TokenKey = bytes.Repeat([]byte{0x42}, 32)
	opts.Now = clock.Now
	opts.Log = slog.New(slog.NewJSONHandler(io.Discard, nil))
	srv, err := server.NewServer(opts)
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	return srv, durable
}

func TestScheduleOnceLocalDir(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)}
	workflows := t.TempDir()
	for name, src := range map[string]string{"ci.yml": scheduleWorkflow, "push.yml": pushOnlyWorkflow} {
		if err := os.WriteFile(filepath.Join(workflows, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	srv, durable := newScheduleTestServer(t, clock, server.Options{
		WorkflowsDir:   workflows,
		ScheduledRepos: []server.ScheduledRepo{{Owner: "o", Name: "r"}},
	})

	n, err := srv.ScheduleOnce(ctx)
	if err != nil || n != 1 {
		t.Fatalf("tick at :00: n=%d err=%v", n, err)
	}
	runs := durable.Runs()
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
	run := runs[0]
	if run.Event.Name != "schedule" || run.Event.Provider != "forgelet" {
		t.Errorf("event = %+v", run.Event)
	}
	jobs, err := durable.ListJobRuns(ctx, run.ID)
	if err != nil || len(jobs) != 1 || jobs[0].JobKey != "nightly" {
		t.Fatalf("schedule run must compile only scheduled workflows: jobs=%d err=%v", len(jobs), err)
	}

	// Same fire time again dedupes.
	if n, err := srv.ScheduleOnce(ctx); err != nil || n != 0 || len(durable.Runs()) != 1 {
		t.Fatalf("replay tick: n=%d runs=%d err=%v", n, len(durable.Runs()), err)
	}

	// Next fire time creates a new run.
	clock.t = clock.t.Add(5 * time.Minute)
	if n, err := srv.ScheduleOnce(ctx); err != nil || n != 1 || len(durable.Runs()) != 2 {
		t.Fatalf(":05 tick: n=%d runs=%d err=%v", n, len(durable.Runs()), err)
	}
}

func TestScheduleOnceUsesDefaultBranch(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{time.Date(2026, 8, 23, 12, 30, 0, 0, time.UTC)}
	src := &fakeGHSource{files: []server.WorkflowFile{{Name: "ci.yml", Data: []byte(scheduleWorkflow)}}}
	srv, durable := newScheduleTestServer(t, clock, server.Options{
		ScheduledRepos:  []server.ScheduledRepo{{Owner: "o", Name: "r"}},
		WorkflowFetcher: src,
	})

	if n, err := srv.ScheduleOnce(ctx); err != nil || n != 1 {
		t.Fatalf("tick: n=%d err=%v", n, err)
	}
	if src.gotRef != "refs/heads/trunk" {
		t.Errorf("workflows fetched at %q, want refs/heads/trunk", src.gotRef)
	}
	run := durable.Runs()[0]
	if run.Event.Ref != "refs/heads/trunk" {
		t.Errorf("event ref = %q", run.Event.Ref)
	}
}

func TestScheduleOnceDisabledWithoutRepos(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)}
	workflows := t.TempDir()
	if err := os.WriteFile(filepath.Join(workflows, "ci.yml"), []byte(scheduleWorkflow), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, durable := newScheduleTestServer(t, clock, server.Options{WorkflowsDir: workflows})
	if n, err := srv.ScheduleOnce(ctx); err != nil || n != 0 || len(durable.Runs()) != 0 {
		t.Fatalf("disabled tick: n=%d runs=%d err=%v", n, len(durable.Runs()), err)
	}
}
