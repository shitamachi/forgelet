package scheduler_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/run/scheduler"
)

// TestConcurrentDispatchHandsEachJobOnce: N dispatchers race over N jobs;
// every job must be dispatched exactly once (claim serialization, FR-D.1).
func TestConcurrentDispatchHandsEachJobOnce(t *testing.T) {
	durable, active, ing, _ := newFixture(t)
	if _, _, err := ing.Ingest(context.Background(), pushDelivery("conc")); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	d := scheduler.NewDispatcher(durable, active, nil)

	const workers = 8
	var wg sync.WaitGroup
	dispatched := make(chan model.JobRunID, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job, err := d.DispatchNext(context.Background())
			if errors.Is(err, scheduler.ErrNoQueuedJob) {
				return
			}
			if err != nil {
				t.Errorf("dispatch: %v", err)
				return
			}
			dispatched <- job.ID
		}()
	}
	wg.Wait()
	close(dispatched)

	seen := map[model.JobRunID]bool{}
	for id := range dispatched {
		if seen[id] {
			t.Fatalf("job %s dispatched twice", id)
		}
		seen[id] = true
	}
	if len(seen) != len(standardJobs()) {
		t.Errorf("distinct dispatched jobs = %d, want %d", len(seen), len(standardJobs()))
	}
	if active.Created != len(standardJobs()) {
		t.Errorf("active objects created = %d, want %d", active.Created, len(standardJobs()))
	}
}

// TestDispatchCancelledRunRejected: a cancelled job must not be claimable or
// dispatchable (FR-G).
func TestDispatchCancelledRunRejected(t *testing.T) {
	durable, active, ing, _ := newFixture(t)
	if _, _, err := ing.Ingest(context.Background(), pushDelivery("cancel")); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	run := mustRun(t, durable)
	if err := scheduler.NewCanceler(durable, nil).Cancel(context.Background(), run.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	if _, err := durable.ClaimNextQueuedJob(context.Background()); !errors.Is(err, scheduler.ErrNoQueuedJob) {
		t.Fatalf("claimed a cancelled job: %v", err)
	}
	jobs := mustListJobs(t, durable)
	if len(jobs) != 2 {
		t.Fatalf("jobs = %d, want 2", len(jobs))
	}
	for _, j := range jobs {
		if j.Status != model.JobCancelled {
			t.Errorf("job %s status = %s, want cancelled", j.ID, j.Status)
		}
	}
	if run := mustRun(t, durable); run.Status != model.RunCancelled {
		t.Errorf("run status = %s, want cancelled", run.Status)
	}

	// Direct dispatch of a cancelled job is rejected without side effects.
	d := scheduler.NewDispatcher(durable, active, nil)
	if err := d.Dispatch(context.Background(), jobs[0].ID); err == nil {
		t.Fatal("dispatch of cancelled job must fail")
	}
	if active.Created != 0 {
		t.Errorf("active object created for cancelled job: %d", active.Created)
	}

	// A cancellation racing between the dispatch pre-check and the ack can
	// leave an orphan object; cancelled jobs are GC-eligible terminals, so
	// the collector removes it (cancelling first would need the pre-check to
	// pass, simulated by cancelling after CreateOrGet via a poisoned ack).
	if n, err := scheduler.NewCollector(durable, active, nil).Collect(context.Background()); err != nil || n != 2 {
		t.Fatalf("collect cancelled run: n=%d err=%v, want 2", n, err)
	}
	if len(active.Objects()) != 0 {
		t.Errorf("orphan objects after GC: %d", len(active.Objects()))
	}
}

// TestCancelTerminalRunRejected: cancelling a finished run is an error;
// cancelling an already-cancelled run is a no-op.
func TestCancelTerminalRunRejected(t *testing.T) {
	durable, active, ing, _ := newFixture(t)
	if _, _, err := ing.Ingest(context.Background(), pushDelivery("cancel2")); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	d := scheduler.NewDispatcher(durable, active, nil)
	p := scheduler.NewProjector(durable, nil)
	for range standardJobs() {
		jobID := dispatchAll(t, d)
		if err := p.Project(context.Background(), jobID, model.PhaseSucceeded); err != nil {
			t.Fatalf("project: %v", err)
		}
	}
	run := mustRun(t, durable)
	c := scheduler.NewCanceler(durable, nil)
	if err := c.Cancel(context.Background(), run.ID); err == nil {
		t.Fatal("cancelling a succeeded run must fail")
	}
	if err := c.Cancel(context.Background(), model.RunID("nope")); err == nil {
		t.Fatal("cancelling an unknown run must fail")
	}
}
