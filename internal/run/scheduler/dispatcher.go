package scheduler

import (
	"context"
	"errors"
	"fmt"

	"github.com/shitamachi/forgelet/internal/run/model"
)

// Dispatcher moves queued JobRuns to the Kubernetes side. The protocol is
// crash-safe across both non-atomic windows:
//
//	W1 durable intent exists, active object not yet created;
//	W2 active object created, dispatch not yet acknowledged.
//
// Replaying Dispatch converges in both: the active object name is derived
// deterministically from the JobRun ID, so CreateOrGet is idempotent, and
// AckDispatched is idempotent for the same object.
type Dispatcher struct {
	durable DurableStore
	active  ActiveExecutionStore
	now     Clock
}

// NewDispatcher wires a Dispatcher.
func NewDispatcher(durable DurableStore, active ActiveExecutionStore, now Clock) *Dispatcher {
	if now == nil {
		now = SystemClock
	}
	return &Dispatcher{durable: durable, active: active, now: now}
}

// DispatchNext claims one queued JobRun and dispatches it. It returns
// ErrNoQueuedJob when nothing is queued. On failure the claim is released so
// the job becomes claimable again, mirroring a released row lock.
func (d *Dispatcher) DispatchNext(ctx context.Context) (model.JobRunRecord, error) {
	job, err := d.durable.ClaimNextQueuedJob(ctx)
	if err != nil {
		return model.JobRunRecord{}, err
	}
	if err := d.Dispatch(ctx, job.ID); err != nil {
		if relErr := d.durable.ReleaseClaim(ctx, job.ID); relErr != nil {
			return model.JobRunRecord{}, fmt.Errorf("dispatch %s: %w", job.ID, errors.Join(err, fmt.Errorf("release claim: %w", relErr)))
		}
		return model.JobRunRecord{}, fmt.Errorf("dispatch %s: %w", job.ID, err)
	}
	return job, nil
}

// Dispatch ensures the active object for a JobRun exists and acknowledges
// the dispatch durably. It is safe to call repeatedly. The job state is
// checked first so cancelled or terminal jobs never get an active object;
// if a cancellation lands between the check and the ack, the ack is refused
// and the orphan object is removed by the collector (cancelled jobs are
// GC-eligible terminals).
func (d *Dispatcher) Dispatch(ctx context.Context, id model.JobRunID) error {
	job, err := d.durable.GetJobRun(ctx, id)
	if err != nil {
		return fmt.Errorf("load %s before dispatch: %w", id, err)
	}
	if job.Status != model.JobQueued && job.Status != model.JobDispatched {
		return fmt.Errorf("%w: %s in status %s", ErrJobNotDispatchable, id, job.Status)
	}
	obj, err := d.active.CreateOrGet(ctx, id)
	if err != nil {
		return fmt.Errorf("ensure active object for %s: %w", id, err)
	}
	if err := d.durable.AckDispatched(ctx, id, ActiveObject{Name: id.CRName(), UID: obj.UID}, d.now()); err != nil {
		return fmt.Errorf("ack dispatch %s: %w", id, err)
	}
	return nil
}
