package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/executor"
	"github.com/shitamachi/forgelet/internal/run"
)

func TestLiveProgressRealHTTPAndSQLiteBeforeFinalResult(t *testing.T) {
	h := newHarness(t, 1)
	item := h.manual(`on: push
jobs:
  test:
    runs-on: small
    steps:
      - id: first
        run: |
          printf '::add-mask::private-progress-token\n'
          printf '::warning title=Coverage::upload private-progress-token was rejected%%0Acheck credentials\n'
          sleep 2
      - id: skip
        if: false
        run: echo must-not-run
`)
	h.tick()
	job, err := h.store.GetJob(t.Context(), item.Jobs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("token-"+job.ID), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	done := make(chan error, 1)
	workspace := t.TempDir()
	go func() {
		done <- executor.RunRemote(ctx, executor.RemoteOptions{URL: h.http.URL, JobID: job.ID, TokenFile: token, Workspace: workspace, Client: h.http.Client(), PollInterval: 10 * time.Millisecond})
	}()
	observed := false
	for ctx.Err() == nil {
		current, err := h.store.GetJob(t.Context(), job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Progress != nil && len(current.Progress.Annotations) == 1 && current.Result == nil {
			if current.Progress.Phase != "executing" || current.Progress.Steps[0].State != run.Running {
				t.Fatalf("not real live state: %+v", current.Progress)
			}
			if current.Progress.Annotations[0].Message != "upload *** was rejected\ncheck credentials" {
				t.Fatal("annotation not decoded/redacted", current.Progress.Annotations)
			}
			observed = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !observed {
		t.Fatal("no live progress before result")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	current, err := h.store.GetJob(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != run.Succeeded || current.Progress.Phase != "cleanup" || current.Result.LastProgressSeq != current.Progress.Seq || current.Progress.Steps[1].State != run.Skipped {
		t.Fatalf("bad final progress: %+v", current)
	}
	if current.ReservedAt.IsZero() || current.ClaimedAt.Before(current.ReservedAt) {
		t.Fatal("scheduler/claim timings not retained")
	}
	// Another Pod cannot replay even an otherwise identical event.
	if code, _ := h.runtimeRequest("POST", "progress", run.Job{ID: job.ID, PodName: job.PodName, PodUID: "wrong"}, run.ProgressEvent{Seq: 1, Kind: "phase", Phase: "executing", At: time.Now()}); code == 200 {
		t.Fatal("accepted wrong workload identity")
	}
}
