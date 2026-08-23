// Package scheduler implements the durable scheduling use cases: ingestion,
// dispatch, observed-status projection, cancellation and CR garbage
// collection. Stores are consumer-side ports; adapters live elsewhere.
package scheduler

import (
	"context"
	"errors"
	"time"

	"github.com/shitamachi/forgelet/internal/run/model"
)

// ActiveObject identifies the Kubernetes resource backing a JobRun.
type ActiveObject struct {
	Name string
	UID  string
}

// DurableStore is the durable scheduling and history port (PostgreSQL in
// production). All methods must be safe for concurrent use and must behave
// idempotently under at-least-once invocation.
type DurableStore interface {
	// RecordDelivery durably records a delivery keyed by (provider, delivery
	// ID). If the key exists it returns the existing record with created=false
	// and never overwrites it.
	RecordDelivery(ctx context.Context, d model.Delivery) (rec model.DeliveryRecord, created bool, err error)

	// CreateRun atomically persists a RunRecord plus one JobRunRecord per
	// seed job and binds the delivery to the run. It is create-or-get keyed
	// by the delivery: replaying the same seed returns the existing run with
	// created=false and performs no writes.
	CreateRun(ctx context.Context, seed model.RunSeed, now time.Time) (run model.RunRecord, created bool, err error)

	GetRun(ctx context.Context, id model.RunID) (model.RunRecord, error)

	GetJobRun(ctx context.Context, id model.JobRunID) (model.JobRunRecord, error)

	ListJobRuns(ctx context.Context, run model.RunID) ([]model.JobRunRecord, error)

	// CountQueuedJobs reports how many JobRuns are waiting for dispatch
	// (observability: queue depth, spec 0010 FR-O3).
	CountQueuedJobs(ctx context.Context) (int, error)

	// ClaimNextQueuedJob returns the oldest queued JobRun without changing
	// its status. Concurrent claims must not hand the same JobRun to two
	// callers before the claim is released. Returns ErrNoQueuedJob when idle.
	ClaimNextQueuedJob(ctx context.Context) (model.JobRunRecord, error)

	// ReleaseClaim drops a claim taken by ClaimNextQueuedJob when dispatch
	// fails before acknowledgement, making the job claimable again. It
	// mirrors a database row lock being released at transaction end.
	ReleaseClaim(ctx context.Context, id model.JobRunID) error

	// AckDispatched records the active object of a dispatched JobRun and
	// moves it queued -> dispatched. Idempotent for the same object name on
	// a non-terminal job; rejected for terminal or cancelled jobs.
	AckDispatched(ctx context.Context, id model.JobRunID, obj ActiveObject, now time.Time) error

	// ApplyObserved projects an observed Kubernetes phase onto the durable
	// JobRun monotonically. Replaying the same or an earlier phase is a
	// no-op success. The WorkflowRun aggregate is refreshed on change.
	ApplyObserved(ctx context.Context, id model.JobRunID, phase model.ObservedPhase, now time.Time) error

	// CancelRun cancels a non-terminal run and all of its non-terminal jobs.
	// Terminal jobs are left untouched. Idempotent for an already-cancelled run.
	CancelRun(ctx context.Context, id model.RunID, now time.Time) error

	// ListGCReadyJobs returns JobRuns whose run and job are both durable
	// terminal and whose active object has not been collected yet.
	ListGCReadyJobs(ctx context.Context) ([]model.JobRunRecord, error)

	// MarkCollected records that the active object of a terminal JobRun has
	// been deleted, so it is not re-scanned. If the delete succeeded but the
	// mark failed, the next pass repeats the idempotent delete and marks again.
	MarkCollected(ctx context.Context, id model.JobRunID, now time.Time) error
}

// ActiveExecutionStore is the Kubernetes-side port (implemented by the CRD
// controller in spec 0004).
type ActiveExecutionStore interface {
	// CreateOrGet ensures the deterministic active object for a JobRun
	// exists and returns it. Calling twice must return the same object and
	// must not create a second one.
	CreateOrGet(ctx context.Context, id model.JobRunID) (ActiveObject, error)

	// Delete removes the active object. Deleting a missing object is a
	// success (idempotent).
	Delete(ctx context.Context, id model.JobRunID) error
}

// IDSource mints run and job run IDs. Both IDGen and FixedIDGen implement it.
type IDSource interface {
	NewRunID() model.RunID
	NewJobRunID() model.JobRunID
}

// Compiler produces the job instances for a triggered event. The real
// implementation is the workflow compiler behind spec 0006.
type Compiler interface {
	Compile(ctx context.Context, ev model.Event, payload []byte) ([]model.JobIntent, error)
}

// ErrNoQueuedJob is returned by ClaimNextQueuedJob when nothing is queued.
var ErrNoQueuedJob = errors.New("scheduler: no queued job run")

// ErrRunNotFound is returned for unknown run IDs.
var ErrRunNotFound = errors.New("scheduler: run not found")

// ErrJobRunNotFound is returned for unknown job run IDs.
var ErrJobRunNotFound = errors.New("scheduler: job run not found")

// ErrJobNotDispatchable is returned when a job cannot be dispatched.
var ErrJobNotDispatchable = errors.New("scheduler: job run not dispatchable")
