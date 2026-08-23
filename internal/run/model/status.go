// Package model defines the provider-neutral durable run state machine and
// record types. Everything here is pure: no clocks, randomness, or I/O.
package model

import "fmt"

// WorkflowRunStatus is the durable status of a WorkflowRun.
type WorkflowRunStatus string

const (
	RunQueued    WorkflowRunStatus = "queued"
	RunRunning   WorkflowRunStatus = "running"
	RunSucceeded WorkflowRunStatus = "succeeded"
	RunFailed    WorkflowRunStatus = "failed"
	RunCancelled WorkflowRunStatus = "cancelled"
)

// IsTerminal reports whether the status is a final state that must never change.
func (s WorkflowRunStatus) IsTerminal() bool {
	switch s {
	case RunSucceeded, RunFailed, RunCancelled:
		return true
	default:
		return false
	}
}

// JobRunStatus is the durable status of a JobRun.
type JobRunStatus string

const (
	JobQueued     JobRunStatus = "queued"
	JobDispatched JobRunStatus = "dispatched"
	JobRunning    JobRunStatus = "running"
	JobSucceeded  JobRunStatus = "succeeded"
	JobFailed     JobRunStatus = "failed"
	JobCancelled  JobRunStatus = "cancelled"
	// JobSkipped marks a job whose dependencies did not succeed and that
	// therefore never executes (GitHub needs semantics; `if: always()` is
	// not in the V1 subset).
	JobSkipped JobRunStatus = "skipped"
)

// IsTerminal reports whether the status is a final state that must never change.
func (s JobRunStatus) IsTerminal() bool {
	switch s {
	case JobSucceeded, JobFailed, JobCancelled, JobSkipped:
		return true
	default:
		return false
	}
}

// jobRank orders non-terminal progress; terminals share the highest rank and
// are distinguished by stickiness instead of ordering.
func jobRank(s JobRunStatus) int {
	switch s {
	case JobQueued:
		return 0
	case JobDispatched:
		return 1
	case JobRunning:
		return 2
	default:
		return 3
	}
}

func runRank(s WorkflowRunStatus) int {
	switch s {
	case RunQueued:
		return 0
	case RunRunning:
		return 1
	default:
		return 2
	}
}

// TransitionError reports an illegal state transition.
type TransitionError struct {
	Kind     string // "jobrun" or "workflowrun"
	From, To string
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("illegal %s transition %s -> %s", e.Kind, e.From, e.To)
}

// CanTransitionJob reports whether from -> to is a legal durable JobRun transition.
// Terminal states are sticky; cancellation is allowed from any non-terminal state.
func CanTransitionJob(from, to JobRunStatus) bool {
	if from.IsTerminal() {
		return false
	}
	switch to {
	case JobCancelled, JobSkipped:
		return true
	case JobDispatched:
		return from == JobQueued
	case JobRunning:
		return from == JobDispatched || from == JobRunning
	case JobSucceeded, JobFailed:
		return from == JobDispatched || from == JobRunning
	default:
		return false
	}
}

// TransitionJob validates from -> to and returns to, or a *TransitionError.
func TransitionJob(from, to JobRunStatus) (JobRunStatus, error) {
	if !CanTransitionJob(from, to) {
		return from, &TransitionError{Kind: "jobrun", From: string(from), To: string(to)}
	}
	return to, nil
}

// CanAdvanceJob reports whether moving from -> to is a forward-only progress
// step used by monotonic projection: to must be at a higher rank, or equal.
// Cancellation is excluded; it is an explicit durable intent, not an observation.
func CanAdvanceJob(from, to JobRunStatus) bool {
	if to == JobCancelled {
		return false
	}
	if from == to {
		return true
	}
	return jobRank(to) > jobRank(from)
}

// CanAdvanceRun reports whether from -> to is forward-only for monotonic run updates.
func CanAdvanceRun(from, to WorkflowRunStatus) bool {
	if from == to {
		return true
	}
	if from.IsTerminal() {
		return false
	}
	if to.IsTerminal() {
		return true
	}
	return runRank(to) > runRank(from)
}

// AggregateRunStatus derives the WorkflowRun status from its JobRun statuses.
// Any non-terminal job keeps the run open; terminal jobs aggregate worst-first.
// Skipped jobs (unsatisfied needs) do not fail the run (GitHub semantics).
func AggregateRunStatus(jobs []JobRunStatus) WorkflowRunStatus {
	running := false
	started := false
	allTerminal := true
	anyFailed, anyCancelled, anySucceeded, anySkipped := false, false, false, false

	for _, s := range jobs {
		switch s {
		case JobRunning:
			running = true
			started = true
			allTerminal = false
		case JobDispatched, JobQueued:
			started = started || s == JobDispatched
			allTerminal = false
		case JobFailed:
			anyFailed = true
		case JobCancelled:
			anyCancelled = true
		case JobSkipped:
			anySkipped = true
		case JobSucceeded:
			anySucceeded = true
		}
	}

	switch {
	case len(jobs) == 0:
		return RunQueued
	case running:
		return RunRunning
	case !allTerminal:
		// Only queued/dispatched jobs remain: dispatched counts as started.
		if started {
			return RunRunning
		}
		return RunQueued
	case anyFailed:
		return RunFailed
	case anyCancelled:
		return RunCancelled
	case anySucceeded, anySkipped:
		return RunSucceeded
	default:
		return RunQueued
	}
}
