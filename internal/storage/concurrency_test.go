package storage

import (
	"testing"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestMatrixQuotaFailFastAndDependencyJoin(t *testing.T) {
	for _, ignore := range []bool{false, true} {
		t.Run(map[bool]string{false: "fail-fast", true: "continue"}[ignore], func(t *testing.T) {
			s := openTest(t)
			createGraph(t, s, "matrix", run.Plan{Key: "test[0]", Group: "test", MatrixTotal: 3, MaxParallel: 1, FailFast: true, ContinueOnError: ignore}, run.Plan{Key: "test[1]", Group: "test", MatrixTotal: 3, MatrixIndex: 1, MaxParallel: 1, FailFast: true}, run.Plan{Key: "test[2]", Group: "test", MatrixTotal: 3, MatrixIndex: 2, MaxParallel: 1, FailFast: true}, run.Plan{Key: "join", Needs: []string{"test"}})
			jobs, err := s.Reserve(t.Context(), 8)
			if err != nil || len(jobs) != 1 {
				t.Fatal(jobs, err)
			}
			completeJob(t, s, jobs[0].ID, run.Failed, nil)
			if ignore {
				for range 2 {
					jobs, err = s.Reserve(t.Context(), 8)
					if err != nil || len(jobs) != 1 || jobs[0].Plan.Key == "join" {
						t.Fatal(jobs, err)
					}
					completeJob(t, s, jobs[0].ID, run.Succeeded, map[string]string{"value": "ok"})
				}
				jobs, err = s.Reserve(t.Context(), 8)
				if err != nil || len(jobs) != 1 || jobs[0].Plan.Key != "join" || jobs[0].Plan.NeedsContext["test"].Result != run.Succeeded {
					t.Fatal(jobs, err)
				}
			} else {
				jobs, err = s.Reserve(t.Context(), 8)
				if err != nil || len(jobs) != 0 {
					t.Fatal(jobs, err)
				}
				r, err := s.GetRun(t.Context(), "matrix")
				if err != nil || r.State != run.Failed {
					t.Fatal(r, err)
				}
				for _, j := range r.Jobs {
					if j.Plan.Key == "join" && j.State != run.Skipped {
						t.Fatal(j)
					}
				}
			}
		})
	}
}
func TestWorkflowConcurrencyPendingReplacementAndCancellation(t *testing.T) {
	s := openTest(t)
	makeRun := func(id string, cancel bool) {
		createGraph(t, s, id, run.Plan{Key: "job", WorkflowConcurrency: "Deploy", WorkflowCancelInProgress: cancel})
	}
	makeRun("a", false)
	jobs, err := s.Reserve(t.Context(), 4)
	if err != nil || len(jobs) != 1 {
		t.Fatal(jobs, err)
	}
	makeRun("b", false)
	makeRun("c", false)
	b, _ := s.GetRun(t.Context(), "b")
	if b.State != run.Cancelled {
		t.Fatal(b)
	}
	if jobs, err = s.Reserve(t.Context(), 4); err != nil || len(jobs) != 0 {
		t.Fatal(jobs, err)
	}
	makeRun("d", true)
	a, _ := s.GetJob(t.Context(), "a-job")
	if !a.CancelRequested || a.State != run.Starting {
		t.Fatal(a)
	}
	if jobs, err = s.Reserve(t.Context(), 4); err != nil || len(jobs) != 0 {
		t.Fatal("released before Pod stop", jobs, err)
	}
	if err := s.Interrupt(t.Context(), a.ID, "stopped"); err != nil {
		t.Fatal(err)
	}
	jobs, err = s.Reserve(t.Context(), 4)
	if err != nil || len(jobs) != 1 || jobs[0].RunID != "d" {
		t.Fatal(jobs, err)
	}
}
func TestJobConcurrencyWaitingAndCaseInsensitivity(t *testing.T) {
	s := openTest(t)
	createGraph(t, s, "a", run.Plan{Key: "first", Concurrency: "Shared"})
	jobs, err := s.Reserve(t.Context(), 4)
	if err != nil || len(jobs) != 1 {
		t.Fatal(jobs, err)
	}
	createGraph(t, s, "b", run.Plan{Key: "second", Concurrency: "shared", CancelInProgress: true})
	jobs, err = s.Reserve(t.Context(), 4)
	if err != nil || len(jobs) != 0 {
		t.Fatal(jobs, err)
	}
	first, _ := s.GetJob(t.Context(), "a-first")
	if !first.CancelRequested {
		t.Fatal(first)
	}
	if err := s.Interrupt(t.Context(), first.ID, "stopped"); err != nil {
		t.Fatal(err)
	}
	jobs, err = s.Reserve(t.Context(), 4)
	if err != nil || len(jobs) != 1 || jobs[0].RunID != "b" {
		t.Fatal(jobs, err)
	}
}

func TestWorkflowKeepsSlotBetweenDependentJobs(t *testing.T) {
	s := openTest(t)
	createGraph(t, s, "old", run.Plan{Key: "a", WorkflowConcurrency: "ci"}, run.Plan{Key: "b", Needs: []string{"a"}, WorkflowConcurrency: "ci"})
	jobs, err := s.Reserve(t.Context(), 4)
	if err != nil || len(jobs) != 1 {
		t.Fatal(jobs, err)
	}
	completeJob(t, s, jobs[0].ID, run.Succeeded, nil)
	createGraph(t, s, "new", run.Plan{Key: "a", WorkflowConcurrency: "ci"})
	old, err := s.GetRun(t.Context(), "old")
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range old.Jobs {
		if j.CancelRequested {
			t.Fatal("partly completed workflow was replaced", j)
		}
	}
	jobs, err = s.Reserve(t.Context(), 4)
	if err != nil || len(jobs) != 1 || jobs[0].RunID != "old" || jobs[0].Plan.Key != "b" {
		t.Fatal(jobs, err)
	}
}

func TestFailFastCancelsRunningSiblingWithNoQueuedJobs(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		state                  run.State
		ignore, failFast, want bool
	}{
		{"failure", run.Failed, false, true, true},
		{"timeout", run.TimedOut, false, true, true},
		{"interruption", run.Interrupted, false, true, true},
		{"ignored ordinary failure", run.Failed, true, true, false},
		{"timeout cannot be ignored", run.TimedOut, true, true, true},
		{"fail fast disabled", run.Failed, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTest(t)
			createGraph(t, s, "all-active", run.Plan{Key: "test[0]", Group: "test", MatrixTotal: 2, MaxParallel: 2, FailFast: tc.failFast, ContinueOnError: tc.ignore}, run.Plan{Key: "test[1]", Group: "test", MatrixIndex: 1, MatrixTotal: 2, MaxParallel: 2, FailFast: tc.failFast, ContinueOnError: tc.ignore})
			jobs, err := s.Reserve(t.Context(), 2)
			if err != nil || len(jobs) != 2 {
				t.Fatal(jobs, err)
			}
			if _, err := s.Claim(t.Context(), jobs[1].ID, "sibling-uid"); err != nil {
				t.Fatal(err)
			}
			completeJob(t, s, jobs[0].ID, tc.state, nil)
			sibling, err := s.GetJob(t.Context(), jobs[1].ID)
			if err != nil || sibling.CancelRequested != tc.want || sibling.State != run.Running {
				t.Fatalf("cancel=%v want=%v state=%s error=%v", sibling.CancelRequested, tc.want, sibling.State, err)
			}
			if tc.want {
				if err := s.Interrupt(t.Context(), sibling.ID, "stopped"); err != nil {
					t.Fatal(err)
				}
				sibling, err = s.GetJob(t.Context(), sibling.ID)
				if err != nil || sibling.State != run.Cancelled {
					t.Fatal(sibling, err)
				}
			}
		})
	}
}
