package storage

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shitamachi/forgelet/internal/run"
)

func createGraph(t *testing.T, s *Store, id string, plans ...run.Plan) map[string]run.Plan {
	t.Helper()
	byKey := make(map[string]run.Plan, len(plans))
	for i := range plans {
		plans[i].JobID, plans[i].RunID = id+"-"+plans[i].Key, id
		plans[i].Profile = "small"
		byKey[plans[i].Key] = plans[i]
	}
	record := run.Run{ID: id, DeliveryID: "delivery-" + id, WorkflowPath: "ci.yml"}
	if ok, err := s.CreateRun(t.Context(), record, plans); err != nil || !ok {
		t.Fatalf("create graph %s: %v, %v", id, ok, err)
	}
	return byKey
}

func completeJob(t *testing.T, s *Store, jobID string, state run.State, outputs map[string]string) {
	t.Helper()
	if _, err := s.Claim(t.Context(), jobID, "uid-"+jobID); err != nil {
		t.Fatal(err)
	}
	if state == run.Interrupted {
		if err := s.Interrupt(t.Context(), jobID, "Pod missing"); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := s.Finish(t.Context(), jobID, "uid-"+jobID, run.Result{State: state, Outputs: outputs}); err != nil {
		t.Fatal(err)
	}
}

func TestZeroCapacityPropagatesFailureWithoutReservingJobs(t *testing.T) {
	s := openTest(t)
	plans := createGraph(t, s, "pressure",
		run.Plan{Key: "build"},
		run.Plan{Key: "test", Needs: []string{"build"}},
		run.Plan{Key: "deploy", Needs: []string{"test"}},
		run.Plan{Key: "cleanup", Needs: []string{"build"}, If: "always()"},
	)
	if jobs, err := s.Reserve(t.Context(), 1); err != nil || len(jobs) != 1 {
		t.Fatalf("reserve build: %v %v", jobs, err)
	}
	completeJob(t, s, plans["build"].JobID, run.Failed, nil)
	for range 2 {
		if jobs, err := s.Reserve(t.Context(), 0); err != nil || len(jobs) != 0 {
			t.Fatalf("zero capacity reserved: %v %v", jobs, err)
		}
	}
	for key, state := range map[string]run.State{"build": run.Failed, "test": run.Skipped, "deploy": run.Skipped, "cleanup": run.Queued} {
		job, err := s.GetJob(t.Context(), plans[key].JobID)
		if err != nil || job.State != state {
			t.Fatalf("%s: %+v %v want %s", key, job, err, state)
		}
	}
	if jobs, err := s.Reserve(t.Context(), 1); err != nil || len(jobs) != 1 || jobs[0].ID != plans["cleanup"].JobID {
		t.Fatalf("resume cleanup: %v %v", jobs, err)
	}
	if _, err := s.Reserve(t.Context(), -1); !errors.Is(err, ErrLimit) {
		t.Fatalf("negative capacity: %v", err)
	}
}

func TestDAGDiamondAndOutputsSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forgelet.db")
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	plans := createGraph(t, s, "diamond", run.Plan{Key: "build"}, run.Plan{Key: "test", Needs: []string{"build"}}, run.Plan{Key: "lint", Needs: []string{"build"}}, run.Plan{Key: "deploy", Needs: []string{"test", "lint"}})
	jobs, err := s.Reserve(t.Context(), 2)
	if err != nil || len(jobs) != 1 || jobs[0].ID != plans["build"].JobID {
		t.Fatalf("root reservation: %+v, %v", jobs, err)
	}
	completeJob(t, s, jobs[0].ID, run.Succeeded, map[string]string{"digest": "sha256:123"})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err = s.Reserve(t.Context(), 2)
	if err != nil || len(jobs) != 2 {
		t.Fatalf("parallel branches after restart: %+v, %v", jobs, err)
	}
	for _, job := range jobs {
		if got := job.Plan.NeedsContext["build"]; got.Result != run.Succeeded || got.Outputs["digest"] != "sha256:123" {
			t.Fatalf("dependency snapshot: %+v", job.Plan)
		}
	}
	completeJob(t, s, jobs[0].ID, run.Succeeded, nil)
	if reserved, err := s.Reserve(t.Context(), 2); err != nil || len(reserved) != 0 {
		t.Fatalf("join ran before both branches: %+v, %v", reserved, err)
	}
	completeJob(t, s, jobs[1].ID, run.Succeeded, nil)
	jobs, err = s.Reserve(t.Context(), 2)
	if err != nil || len(jobs) != 1 || jobs[0].ID != plans["deploy"].JobID {
		t.Fatalf("join reservation: %+v, %v", jobs, err)
	}
	claimed, err := s.Claim(t.Context(), jobs[0].ID, "uid")
	if err != nil || !reflect.DeepEqual(claimed, jobs[0].Plan) {
		t.Fatalf("claim changed dependency snapshot: %+v, %v", claimed, err)
	}
}

func TestDAGFailurePropagationAndConditions(t *testing.T) {
	for _, state := range []run.State{run.Succeeded, run.Failed, run.TimedOut, run.Interrupted, run.Cancelled} {
		t.Run(string(state), func(t *testing.T) {
			s := openTest(t)
			plans := createGraph(t, s, "conditions",
				run.Plan{Key: "root"},
				run.Plan{Key: "normal", Needs: []string{"root"}},
				run.Plan{Key: "failure", Needs: []string{"root"}, If: "failure()"},
				run.Plan{Key: "always", Needs: []string{"root"}, If: "always()"},
				run.Plan{Key: "cancel", Needs: []string{"root"}, If: "cancelled()"},
				run.Plan{Key: "implicit", Needs: []string{"root"}, If: "needs.root.result != 'success'"},
			)
			if _, err := s.Reserve(t.Context(), 4); err != nil {
				t.Fatal(err)
			}
			completeJob(t, s, plans["root"].JobID, state, nil)
			jobs, err := s.Reserve(t.Context(), 10)
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]bool{}
			for _, job := range jobs {
				got[job.Plan.Key] = true
			}
			failure := state == run.Failed || state == run.TimedOut || state == run.Interrupted
			for key, want := range map[string]bool{"normal": state == run.Succeeded, "failure": failure, "always": true, "cancel": state == run.Cancelled, "implicit": false} {
				if got[key] != want {
					t.Errorf("reserved %s=%v, want %v: %+v", key, got[key], want, got)
				}
				job, err := s.GetJob(t.Context(), plans[key].JobID)
				if err != nil {
					t.Fatal(err)
				}
				if !want && (job.State != run.Skipped || job.PodUID != "") {
					t.Errorf("false condition allocated a Pod: %+v", job)
				}
				if job.Plan.AncestorFailed != failure {
					t.Errorf("ancestor failure=%v, want %v", job.Plan.AncestorFailed, failure)
				}
				wantResult := state
				if state == run.TimedOut || state == run.Interrupted {
					wantResult = run.Failed
				}
				if job.Plan.NeedsContext["root"].Result != wantResult {
					t.Errorf("normalized dependency result: %+v", job.Plan.NeedsContext)
				}
			}
		})
	}
}

func TestDAGSkipFixedPointAtFullQuotaAndNoStarvation(t *testing.T) {
	s := openTest(t)
	busy := createTest(t, s, "busy")
	claimTest(t, s, busy)
	plans := createGraph(t, s, "skip-chain",
		run.Plan{Key: "a", Needs: []string{"b"}},
		run.Plan{Key: "b", Needs: []string{"z"}},
		run.Plan{Key: "z", If: "false"},
		run.Plan{Key: "cleanup", Needs: []string{"a"}, If: "always()"},
	)
	jobs, err := s.Reserve(t.Context(), 1)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("full quota: %+v, %v", jobs, err)
	}
	for _, key := range []string{"a", "b", "z"} {
		job, err := s.GetJob(t.Context(), plans[key].JobID)
		if err != nil || job.State != run.Skipped {
			t.Errorf("skip chain did not converge: %+v, %v", job, err)
		}
	}
	if err := s.Finish(t.Context(), busy.JobID, "pod-uid", run.Result{State: run.Succeeded}); err != nil {
		t.Fatal(err)
	}
	jobs, err = s.Reserve(t.Context(), 1)
	if err != nil || len(jobs) != 1 || jobs[0].Plan.Key != "cleanup" {
		t.Fatalf("always after skipped ancestor: %+v, %v", jobs, err)
	}
	// Earlier blocked children must not hide a later unrelated ready run.
	createGraph(t, s, "blocked", run.Plan{Key: "a", Needs: []string{"z"}}, run.Plan{Key: "z"})
	ready := createTest(t, s, "later")
	jobs, err = s.Reserve(t.Context(), 3)
	if err != nil || len(jobs) != 2 || jobs[1].ID != ready.JobID {
		t.Fatalf("blocked child starved ready job: %+v, %v", jobs, err)
	}
}

func TestDAGTransitiveFailureAndCancellation(t *testing.T) {
	s := openTest(t)
	plans := createGraph(t, s, "transitive", run.Plan{Key: "root"}, run.Plan{Key: "middle", Needs: []string{"root"}, If: "always()"}, run.Plan{Key: "leaf", Needs: []string{"middle"}, If: "failure()"})
	_, _ = s.Reserve(t.Context(), 4)
	completeJob(t, s, plans["root"].JobID, run.Failed, nil)
	_, _ = s.Reserve(t.Context(), 4)
	completeJob(t, s, plans["middle"].JobID, run.Succeeded, nil)
	jobs, err := s.Reserve(t.Context(), 4)
	if err != nil || len(jobs) != 1 || !jobs[0].Plan.AncestorFailed || jobs[0].Plan.NeedsContext["middle"].Result != run.Succeeded {
		t.Fatalf("lost transitive failure through successful cleanup: %+v, %v", jobs, err)
	}
	completeJob(t, s, plans["leaf"].JobID, run.Succeeded, nil)
	record, err := s.GetRun(t.Context(), "transitive")
	if err != nil || record.State != run.Failed {
		t.Fatalf("cleanup hid original failure: %+v, %v", record, err)
	}
	plans = createGraph(t, s, "cancel-graph", run.Plan{Key: "root"}, run.Plan{Key: "cleanup", Needs: []string{"root"}, If: "always()"})
	_, _ = s.Reserve(t.Context(), 4)
	if err := s.CancelRun(t.Context(), "cancel-graph"); err != nil {
		t.Fatal(err)
	}
	if err := s.Interrupt(t.Context(), plans["root"].JobID, "cancel confirmed"); err != nil {
		t.Fatal(err)
	}
	jobs, err = s.Reserve(t.Context(), 4)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("cancel allowed cleanup to start: %+v, %v", jobs, err)
	}
	record, _ = s.GetRun(t.Context(), "cancel-graph")
	for _, job := range record.Jobs {
		if job.State != run.Cancelled {
			t.Fatalf("uncancelled graph job: %+v", job)
		}
	}
}

func TestDAGValidationAndAggregate(t *testing.T) {
	for name, plans := range map[string][]run.Plan{
		"missing":   {{Key: "a", JobID: "a", RunID: "r", Needs: []string{"missing"}}},
		"duplicate": {{Key: "a", JobID: "a", RunID: "r"}, {Key: "a", JobID: "b", RunID: "r"}},
		"cycle":     {{Key: "a", JobID: "a", RunID: "r", Needs: []string{"b"}}, {Key: "b", JobID: "b", RunID: "r", Needs: []string{"a"}}},
	} {
		t.Run(name, func(t *testing.T) {
			s := openTest(t)
			if _, err := s.CreateRun(t.Context(), run.Run{ID: "r", DeliveryID: "d", WorkflowPath: "ci.yml"}, plans); !errors.Is(err, ErrConflict) {
				t.Fatalf("invalid graph accepted: %v", err)
			}
			if _, err := s.GetRun(t.Context(), "r"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("partial invalid graph persisted: %v", err)
			}
		})
	}
	for _, tc := range []struct {
		states []run.State
		want   run.State
	}{
		{[]run.State{run.Skipped, run.Skipped}, run.Skipped},
		{[]run.State{run.Skipped, run.Failed}, run.Failed},
		{[]run.State{run.Skipped, run.Succeeded}, run.Succeeded},
		{[]run.State{run.Skipped, run.Queued}, run.Running},
	} {
		var jobs []run.Job
		for _, state := range tc.states {
			jobs = append(jobs, run.Job{State: state})
		}
		if got := aggregate(jobs); got != tc.want {
			t.Errorf("aggregate %v=%s, want %s", tc.states, got, tc.want)
		}
	}
}

func TestResultOutputLimitsAndStepOutcome(t *testing.T) {
	s := openTest(t)
	p := createTest(t, s, "bounded-outputs")
	claimTest(t, s, p)
	tooMany := map[string]string{}
	for i := range 101 {
		tooMany[fmt.Sprint(i)] = "x"
	}
	for name, result := range map[string]run.Result{
		"job bytes":     {State: run.Succeeded, Outputs: map[string]string{"x": strings.Repeat("x", 64<<10)}},
		"job count":     {State: run.Succeeded, Outputs: tooMany},
		"step count":    {State: run.Succeeded, Steps: []run.StepResult{{ID: "a", State: run.Succeeded, Outputs: tooMany}}},
		"step combined": {State: run.Succeeded, Steps: []run.StepResult{{ID: "a", State: run.Succeeded, Outputs: map[string]string{"x": strings.Repeat("x", 32<<10)}}, {ID: "b", State: run.Succeeded, Outputs: map[string]string{"x": strings.Repeat("x", 32<<10)}}}},
	} {
		if err := s.Finish(t.Context(), p.JobID, "pod-uid", result); !errors.Is(err, ErrLimit) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	if err := s.Finish(t.Context(), p.JobID, "pod-uid", run.Result{State: run.Succeeded, Steps: []run.StepResult{{ID: "a", State: run.Running}}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("nonterminal step accepted: %v", err)
	}
	result := run.Result{State: run.Succeeded, Outputs: map[string]string{"version": "1"}, Steps: []run.StepResult{{ID: "a", State: run.Succeeded, Outcome: run.Failed}, {ID: "b", State: run.Skipped, Outcome: run.Skipped}}}
	if err := s.Finish(t.Context(), p.JobID, "pod-uid", result); err != nil {
		t.Fatal(err)
	}
}
