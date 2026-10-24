package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/executor"
	"github.com/shitamachi/forgelet/internal/run"
)

func TestDAGThroughHTTPExecutorAndSQLite(t *testing.T) {
	h := newHarness(t, 2)
	source, err := os.ReadFile("../../test/fixtures/conditions.yml")
	if err != nil {
		t.Fatal(err)
	}
	record := h.manual(strings.ReplaceAll(string(source), "runs-on: homelab", "runs-on: small"))
	for attempts := 0; attempts < 20; attempts++ {
		h.tick()
		current, err := h.store.GetRun(t.Context(), record.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.State.Terminal() {
			record = current
			break
		}
		for _, job := range current.Jobs {
			if job.State != run.Starting {
				continue
			}
			dir := t.TempDir()
			tokenFile := filepath.Join(dir, "token")
			if err := os.WriteFile(tokenFile, []byte("token-"+job.ID), 0600); err != nil {
				t.Fatal(err)
			}
			runErr := executor.RunRemote(t.Context(), executor.RemoteOptions{URL: h.http.URL, JobID: job.ID, PodUID: job.PodUID, TokenFile: tokenFile, Workspace: filepath.Join(dir, "workspace"), RetryTimeout: time.Second, PollInterval: 10 * time.Millisecond})
			finished, err := h.store.GetJob(t.Context(), job.ID)
			if err != nil || !finished.State.Terminal() {
				t.Fatalf("job %s failed to report result: job=%+v, remote=%v, storage=%v", job.Plan.Key, finished, runErr, err)
			}
		}
	}
	if record.State != run.Failed {
		t.Fatalf("cleanup success must not erase failure: %+v", record)
	}
	want := map[string]run.State{"seed": run.Succeeded, "build": run.Failed, "blocked": run.Skipped, "transitive": run.Succeeded, "report": run.Succeeded, "success_path": run.Succeeded, "false_job": run.Skipped, "not_after_skip": run.Skipped, "always_after_skip": run.Succeeded}
	for _, job := range record.Jobs {
		if job.State != want[job.Plan.Key] {
			t.Fatalf("job %s: state=%s result=%+v", job.Plan.Key, job.State, job.Result)
		}
		if job.State == run.Skipped && job.PodUID != "" {
			t.Fatalf("skipped job created a Pod: %+v", job)
		}
		if job.Plan.Key == "build" {
			steps := job.Result.Steps
			if len(steps) != 5 || steps[0].Outcome != run.Failed || steps[0].State != run.Succeeded || steps[2].State != run.Skipped || steps[3].State != run.Succeeded || steps[4].State != run.Succeeded {
				t.Fatalf("step propagation=%+v", steps)
			}
		}
	}
	if h.runtime.created != 6 {
		t.Fatalf("expected only six eligible Pods, created=%d", h.runtime.created)
	}
	// A rerun must use the new dependency attempts, not a persisted old context.
	status, body := h.admin(http.MethodPost, "/api/runs/"+record.ID+"/rerun", nil)
	if status != http.StatusCreated {
		t.Fatalf("rerun=%d %s", status, body)
	}
	var rerun run.Run
	if err := json.Unmarshal(body, &rerun); err != nil {
		t.Fatal(err)
	}
	for _, job := range rerun.Jobs {
		if len(job.Plan.NeedsContext) != 0 || job.Plan.AncestorFailed || job.RunID == record.ID {
			t.Fatalf("rerun reused resolved state: %+v", job)
		}
	}
	h.tick()
	rerun, err = h.store.GetRun(t.Context(), rerun.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range rerun.Jobs {
		if job.Plan.Key == "build" && job.State != run.Queued {
			t.Fatalf("rerun build did not wait for new seed: %+v", job)
		}
	}
}
