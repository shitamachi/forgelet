package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run/model"
)

func TestPGRerequestCreatesNewAttempt(t *testing.T) {
	s, _ := testDatabase(t)
	ctx := context.Background()
	runID := ingest(t, s, "rereq-1")
	jobs, _ := s.ListJobRuns(ctx, runID)
	orig := jobs[0]
	// Fail the original
	if err := s.ApplyObserved(ctx, orig.ID, model.PhaseFailed, time.Unix(1_700_000_100, 0).UTC()); err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	newID, err := s.RerequestJob(ctx, orig.ID, time.Unix(1_700_000_200, 0).UTC())
	if err != nil {
		t.Fatalf("rerequest: %v", err)
	}
	if newID == orig.ID {
		t.Fatal("new ID same as orig")
	}
	newRec, err := s.GetJobRun(ctx, newID)
	if err != nil {
		t.Fatalf("get new: %v", err)
	}
	if newRec.Attempt != 2 || newRec.Status != model.JobQueued {
		t.Errorf("new job attempt=%d status=%s, want 2 queued", newRec.Attempt, newRec.Status)
	}
	// Idempotent second call
	newID2, err := s.RerequestJob(ctx, orig.ID, time.Unix(1_700_000_200, 0).UTC())
	if err != nil || newID2 != newID {
		t.Fatalf("idempotent: %v %q want %q", err, newID2, newID)
	}
	// Run should be reopened
	run, _ := s.GetRun(ctx, runID)
	if run.Status.IsTerminal() {
		t.Errorf("run should be reopened, got %s", run.Status)
	}
	jobs, _ = s.ListJobRuns(ctx, runID)
	if len(jobs) != 3 { // original test has 2 jobs, plus new attempt makes 3
		// For this ingest, pgJobs has 2 jobs, so after rerequest should be 3
		t.Errorf("jobs after rerequest=%d, want 3", len(jobs))
	}
}
