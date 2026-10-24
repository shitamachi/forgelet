package storage

import (
	"testing"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestFinishDoesNotHideExecutorErrorWithContinueOnError(t *testing.T) {
	s := openTest(t)
	plans := createGraph(t, s, "coe-error", run.Plan{Key: "coe", ContinueOnError: true})
	jobID := plans["coe"].JobID
	if jobs, err := s.Reserve(t.Context(), 4); err != nil || len(jobs) != 1 {
		t.Fatalf("reserve: %v %v", jobs, err)
	}
	if _, err := s.Claim(t.Context(), jobID, "uid-"+jobID); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(t.Context(), jobID, "uid-"+jobID, run.Result{State: run.Failed, Error: "executor error"}); err != nil {
		t.Fatal(err)
	}
	job, err := s.GetJob(t.Context(), jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != run.Failed || job.Result == nil || job.Result.State != run.Failed {
		t.Fatalf("infrastructure failure was converted: %+v", job)
	}
}
