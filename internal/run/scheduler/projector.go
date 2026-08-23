package scheduler

import (
	"context"
	"fmt"

	"github.com/shitamachi/forgelet/internal/run/model"
)

// Projector maps observed Kubernetes execution state onto durable state.
// Window W3 (observed change happened, projection not yet durable) converges
// because ApplyObserved is monotonic and idempotent: replaying the same or an
// earlier phase is a no-op success.
type Projector struct {
	durable DurableStore
	now     Clock
}

// NewProjector wires a Projector.
func NewProjector(durable DurableStore, now Clock) *Projector {
	if now == nil {
		now = SystemClock
	}
	return &Projector{durable: durable, now: now}
}

// Project applies an observed phase to a JobRun. PhasePending is accepted
// and ignored (it never moves durable state backwards).
func (p *Projector) Project(ctx context.Context, id model.JobRunID, phase model.ObservedPhase) error {
	t := p.now()
	if err := p.durable.ApplyObserved(ctx, id, phase, t); err != nil {
		return fmt.Errorf("apply observed %s to %s: %w", phase, id, err)
	}
	return nil
}

// Collector garbage-collects active objects whose durable state is fully
// terminal. Window W4 (run terminal in PG, active object still present)
// converges because collection only runs after ListGCReadyJobs reports the
// job, Delete is idempotent, and a successful delete is recorded with
// MarkCollected. If the process dies between delete and mark, the next pass
// repeats the idempotent delete. Terminal state is never lost: the durable
// record is the authority and is untouched by collection.
type Collector struct {
	durable DurableStore
	active  ActiveExecutionStore
	now     Clock
}

// NewCollector wires a Collector.
func NewCollector(durable DurableStore, active ActiveExecutionStore, now Clock) *Collector {
	if now == nil {
		now = SystemClock
	}
	return &Collector{durable: durable, active: active, now: now}
}

// Collect removes the active objects of all GC-ready JobRuns and returns how
// many were collected. Missing objects count as collected.
func (c *Collector) Collect(ctx context.Context) (int, error) {
	jobs, err := c.durable.ListGCReadyJobs(ctx)
	if err != nil {
		return 0, fmt.Errorf("list gc-ready jobs: %w", err)
	}
	collected := 0
	for _, j := range jobs {
		if err := c.active.Delete(ctx, j.ID); err != nil {
			return collected, fmt.Errorf("delete active object %s: %w", j.ID, err)
		}
		if err := c.durable.MarkCollected(ctx, j.ID, c.now()); err != nil {
			return collected, fmt.Errorf("mark collected %s: %w", j.ID, err)
		}
		collected++
	}
	return collected, nil
}

// Canceler cancels runs durably. Terminal jobs are never rewritten.
type Canceler struct {
	durable DurableStore
	now     Clock
}

// NewCanceler wires a Canceler.
func NewCanceler(durable DurableStore, now Clock) *Canceler {
	if now == nil {
		now = SystemClock
	}
	return &Canceler{durable: durable, now: now}
}

// Cancel cancels a run and its non-terminal jobs. Cancelling an already
// terminal run is an error; cancelling an already-cancelled one is a no-op.
func (c *Canceler) Cancel(ctx context.Context, id model.RunID) error {
	if err := c.durable.CancelRun(ctx, id, c.now()); err != nil {
		return fmt.Errorf("cancel run %s: %w", id, err)
	}
	return nil
}
