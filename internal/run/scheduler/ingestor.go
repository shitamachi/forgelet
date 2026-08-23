package scheduler

import (
	"context"
	"fmt"

	"github.com/shitamachi/forgelet/internal/run/model"
)

// Ingestor turns an incoming provider delivery into a durable WorkflowRun,
// deduplicating by delivery key before any expensive work.
type Ingestor struct {
	store    DurableStore
	compiler Compiler
	ids      IDSource
	now      Clock
}

// NewIngestor wires an Ingestor. ids and now must be non-nil.
func NewIngestor(store DurableStore, compiler Compiler, ids IDSource, now Clock) *Ingestor {
	if now == nil {
		now = SystemClock
	}
	return &Ingestor{store: store, compiler: compiler, ids: ids, now: now}
}

// Ingest records the delivery durably, then — only for first delivery —
// compiles jobs and creates the run. Replay of a known delivery returns the
// existing run with created=false and never invokes the compiler. If a
// previous attempt recorded the delivery but crashed before creating the run
// (window W0), the replay completes the creation exactly once.
func (i *Ingestor) Ingest(ctx context.Context, d model.Delivery) (model.RunID, bool, error) {
	rec, _, err := i.store.RecordDelivery(ctx, d)
	if err != nil {
		return "", false, fmt.Errorf("record delivery %s: %w", d.Key, err)
	}
	if rec.RunID != "" {
		return rec.RunID, false, nil
	}

	jobs, err := i.compiler.Compile(ctx, d.Event, rec.Payload)
	if err != nil {
		// The delivery receipt stays durable for audit and retry; no run yet.
		return "", false, fmt.Errorf("compile for delivery %s: %w", d.Key, err)
	}
	for _, j := range jobs {
		if err := j.Validate(); err != nil {
			return "", false, fmt.Errorf("delivery %s: %w", d.Key, err)
		}
	}

	seed := model.RunSeed{Delivery: d.Key, Event: d.Event, Jobs: jobs}
	run, createdRun, err := i.store.CreateRun(ctx, seed, i.now())
	if err != nil {
		return "", false, fmt.Errorf("create run for delivery %s: %w", d.Key, err)
	}
	return run.ID, createdRun, nil
}
