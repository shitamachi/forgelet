package model

import (
	"errors"
	"strings"
	"testing"
)

func TestCanTransitionJob(t *testing.T) {
	cases := []struct {
		from, to JobRunStatus
		want     bool
	}{
		{JobQueued, JobDispatched, true},
		{JobQueued, JobRunning, false},   // must pass through dispatched
		{JobQueued, JobSucceeded, false}, // observation cannot skip dispatch
		{JobDispatched, JobRunning, true},
		{JobDispatched, JobSucceeded, true},
		{JobDispatched, JobFailed, true},
		{JobDispatched, JobQueued, false}, // no backwards
		{JobRunning, JobRunning, true},    // sticky-allowed refresh
		{JobRunning, JobSucceeded, true},
		{JobRunning, JobFailed, true},
		{JobRunning, JobDispatched, false},
		{JobQueued, JobCancelled, true},
		{JobDispatched, JobCancelled, true},
		{JobRunning, JobCancelled, true},
		{JobSucceeded, JobCancelled, false}, // terminal is sticky
		{JobFailed, JobRunning, false},
		{JobCancelled, JobQueued, false},
		{JobSucceeded, JobFailed, false}, // terminal cannot switch outcome
		{JobFailed, JobSucceeded, false},
	}
	for _, tc := range cases {
		if got := CanTransitionJob(tc.from, tc.to); got != tc.want {
			t.Errorf("CanTransitionJob(%s, %s) = %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}

func TestTransitionJobError(t *testing.T) {
	_, err := TransitionJob(JobSucceeded, JobRunning)
	if err == nil {
		t.Fatal("expected error for terminal -> running")
	}
	var te *TransitionError
	if !errors.As(err, &te) {
		t.Fatalf("error %v is not *TransitionError", err)
	}
	if te.From != string(JobSucceeded) || te.To != string(JobRunning) {
		t.Errorf("unexpected error fields: %+v", te)
	}
	if !strings.Contains(err.Error(), "succeeded") || !strings.Contains(err.Error(), "running") {
		t.Errorf("error message %q lacks statuses", err.Error())
	}
}

func TestCanAdvanceJob(t *testing.T) {
	cases := []struct {
		from, to JobRunStatus
		want     bool
	}{
		{JobQueued, JobQueued, true},
		{JobQueued, JobDispatched, true},
		{JobQueued, JobRunning, true},     // observation may jump ahead of ack
		{JobQueued, JobSucceeded, true},   // very fast pod: projection skips
		{JobDispatched, JobQueued, false}, // never backwards
		{JobRunning, JobDispatched, false},
		{JobSucceeded, JobFailed, false}, // terminal sticky
		{JobSucceeded, JobSucceeded, true},
		{JobQueued, JobCancelled, false}, // cancel is intent, not observation
		{JobRunning, JobCancelled, false},
	}
	for _, tc := range cases {
		if got := CanAdvanceJob(tc.from, tc.to); got != tc.want {
			t.Errorf("CanAdvanceJob(%s, %s) = %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}

func TestCanAdvanceRun(t *testing.T) {
	cases := []struct {
		from, to WorkflowRunStatus
		want     bool
	}{
		{RunQueued, RunRunning, true},
		{RunQueued, RunSucceeded, true},
		{RunRunning, RunQueued, false},
		{RunRunning, RunFailed, true},
		{RunSucceeded, RunFailed, false},
		{RunCancelled, RunRunning, false},
		{RunRunning, RunRunning, true},
	}
	for _, tc := range cases {
		if got := CanAdvanceRun(tc.from, tc.to); got != tc.want {
			t.Errorf("CanAdvanceRun(%s, %s) = %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}

func TestAggregateRunStatus(t *testing.T) {
	cases := []struct {
		name string
		jobs []JobRunStatus
		want WorkflowRunStatus
	}{
		{"empty", nil, RunQueued},
		{"all queued", []JobRunStatus{JobQueued, JobQueued}, RunQueued},
		{"one dispatched", []JobRunStatus{JobQueued, JobDispatched}, RunRunning},
		{"one running", []JobRunStatus{JobRunning, JobQueued}, RunRunning},
		{"all succeeded", []JobRunStatus{JobSucceeded, JobSucceeded}, RunSucceeded},
		{"any failed wins", []JobRunStatus{JobSucceeded, JobFailed}, RunFailed},
		{"cancelled over succeeded", []JobRunStatus{JobSucceeded, JobCancelled}, RunCancelled},
		{"failed over cancelled", []JobRunStatus{JobCancelled, JobFailed}, RunFailed},
		{"terminal plus running", []JobRunStatus{JobSucceeded, JobRunning}, RunRunning},
		{"single cancelled", []JobRunStatus{JobCancelled}, RunCancelled},
	}
	for _, tc := range cases {
		if got := AggregateRunStatus(tc.jobs); got != tc.want {
			t.Errorf("%s: AggregateRunStatus = %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestJobRunIDCRName(t *testing.T) {
	id := JobRunID("01JABCDEF0123456789ABCDEFG")
	if got, want := id.CRName(), "jobrun-01jabcdef0123456789abcdefg"; got != want {
		t.Errorf("CRName() = %q, want %q", got, want)
	}
}

func TestJobStatusFromPhase(t *testing.T) {
	cases := []struct {
		phase   ObservedPhase
		want    JobRunStatus
		changes bool
	}{
		{PhasePending, "", false},
		{PhaseRunning, JobRunning, true},
		{PhaseSucceeded, JobSucceeded, true},
		{PhaseFailed, JobFailed, true},
		{ObservedPhase("weird"), "", false},
	}
	for _, tc := range cases {
		got, changes := JobStatusFromPhase(tc.phase)
		if got != tc.want || changes != tc.changes {
			t.Errorf("JobStatusFromPhase(%s) = (%s, %v), want (%s, %v)", tc.phase, got, changes, tc.want, tc.changes)
		}
	}
}

func TestJobIntentValidate(t *testing.T) {
	if err := (JobIntent{JobKey: "  ", RunnerClass: "x"}).Validate(); err == nil {
		t.Error("blank job key must fail")
	}
	if err := (JobIntent{JobKey: "build", RunnerClass: ""}).Validate(); err == nil {
		t.Error("blank runner class must fail")
	}
	if err := (JobIntent{JobKey: "build", RunnerClass: "small"}).Validate(); err != nil {
		t.Errorf("valid intent rejected: %v", err)
	}
}
