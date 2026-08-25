package scheduler_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/run/scheduler"
	"github.com/shitamachi/forgelet/internal/storage/memory"
)

// failingActive fails the first N calls to CreateOrGet, then delegates.
type failingActive struct {
	inner   *memory.ActiveStore
	failGet int // remaining CreateOrGet failures to inject
}

func (f *failingActive) CreateOrGet(ctx context.Context, id model.JobRunID) (scheduler.ActiveObject, error) {
	if f.failGet > 0 {
		f.failGet--
		return scheduler.ActiveObject{}, errors.New("injected: create failed")
	}
	return f.inner.CreateOrGet(ctx, id)
}

func (f *failingActive) Delete(ctx context.Context, id model.JobRunID) error {
	return f.inner.Delete(ctx, id)
}

// failingDurable fails the first N calls to the wrapped method.
type failingDurable struct {
	inner     *memory.DurableStore
	failAck   int
	failApply int
}

func (f *failingDurable) RecordDelivery(ctx context.Context, d model.Delivery) (model.DeliveryRecord, bool, error) {
	return f.inner.RecordDelivery(ctx, d)
}
func (f *failingDurable) CreateRun(ctx context.Context, seed model.RunSeed, now time.Time) (model.RunRecord, bool, error) {
	return f.inner.CreateRun(ctx, seed, now)
}
func (f *failingDurable) GetRun(ctx context.Context, id model.RunID) (model.RunRecord, error) {
	return f.inner.GetRun(ctx, id)
}
func (f *failingDurable) GetJobRun(ctx context.Context, id model.JobRunID) (model.JobRunRecord, error) {
	return f.inner.GetJobRun(ctx, id)
}
func (f *failingDurable) ListJobRuns(ctx context.Context, run model.RunID) ([]model.JobRunRecord, error) {
	return f.inner.ListJobRuns(ctx, run)
}
func (f *failingDurable) CountQueuedJobs(ctx context.Context) (int, error) {
	return f.inner.CountQueuedJobs(ctx)
}
func (f *failingDurable) ListQueuedJobs(ctx context.Context) ([]model.JobRunRecord, error) {
	return f.inner.ListQueuedJobs(ctx)
}
func (f *failingDurable) ClaimNextQueuedJob(ctx context.Context) (model.JobRunRecord, error) {
	return f.inner.ClaimNextQueuedJob(ctx)
}
func (f *failingDurable) ReleaseClaim(ctx context.Context, id model.JobRunID) error {
	return f.inner.ReleaseClaim(ctx, id)
}
func (f *failingDurable) AckDispatched(ctx context.Context, id model.JobRunID, obj scheduler.ActiveObject, now time.Time) error {
	if f.failAck > 0 {
		f.failAck--
		return errors.New("injected: ack failed")
	}
	return f.inner.AckDispatched(ctx, id, obj, now)
}
func (f *failingDurable) ApplyObserved(ctx context.Context, id model.JobRunID, phase model.ObservedPhase, now time.Time) error {
	if f.failApply > 0 {
		f.failApply--
		return errors.New("injected: apply failed")
	}
	return f.inner.ApplyObserved(ctx, id, phase, now)
}
func (f *failingDurable) CancelRun(ctx context.Context, id model.RunID, now time.Time) error {
	return f.inner.CancelRun(ctx, id, now)
}
func (f *failingDurable) ListGCReadyJobs(ctx context.Context) ([]model.JobRunRecord, error) {
	return f.inner.ListGCReadyJobs(ctx)
}
func (f *failingDurable) MarkCollected(ctx context.Context, id model.JobRunID, now time.Time) error {
	return f.inner.MarkCollected(ctx, id, now)
}

func dispatchAll(t *testing.T, d *scheduler.Dispatcher) model.JobRunID {
	t.Helper()
	job, err := d.DispatchNext(context.Background())
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	return job.ID
}

func jobStatus(t *testing.T, s *memory.DurableStore, id model.JobRunID) model.JobRunStatus {
	t.Helper()
	job, err := s.GetJobRun(context.Background(), id)
	if err != nil {
		t.Fatalf("get job %s: %v", id, err)
	}
	return job.Status
}

// TestWindow1_CRCreateFails: PG intent committed, CR creation fails. Replay
// must converge with exactly one active object ever created.
func TestWindow1_CRCreateFails(t *testing.T) {
	durable, active, ing, _ := newFixture(t)
	if _, _, err := ing.Ingest(context.Background(), pushDelivery("w1")); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	flaky := &failingActive{inner: active, failGet: 1}
	d := scheduler.NewDispatcher(durable, flaky, nil)
	if _, err := d.DispatchNext(context.Background()); err == nil {
		t.Fatal("expected injected failure")
	}
	if active.Created != 0 {
		t.Fatalf("no object should exist after failed create, got %d", active.Created)
	}

	// Replay with a healthy active store.
	d2 := scheduler.NewDispatcher(durable, active, nil)
	jobID := dispatchAll(t, d2)
	if active.Created != 1 {
		t.Errorf("exactly one object must be created across retries, got %d", active.Created)
	}
	if got := jobStatus(t, durable, jobID); got != model.JobDispatched {
		t.Errorf("job status = %s, want dispatched", got)
	}
	if len(active.Objects()) != 1 {
		t.Errorf("active objects = %d, want 1", len(active.Objects()))
	}
}

// TestWindow2_AckFailsAfterCreate: CR created, PG acknowledgement fails.
// Replay must adopt the existing object, not create a second one.
func TestWindow2_AckFailsAfterCreate(t *testing.T) {
	durable, active, ing, _ := newFixture(t)
	if _, _, err := ing.Ingest(context.Background(), pushDelivery("w2")); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	flaky := &failingDurable{inner: durable, failAck: 1}
	d := scheduler.NewDispatcher(flaky, active, nil)
	if _, err := d.DispatchNext(context.Background()); err == nil {
		t.Fatal("expected injected ack failure")
	}
	if active.Created != 1 {
		t.Fatalf("object should exist despite ack failure, got %d", active.Created)
	}
	if got := jobStatus(t, durable, mustFirstJob(t, durable)); got != model.JobQueued {
		t.Errorf("job must remain queued when ack failed, got %s", got)
	}

	// Replay converges without creating a second object.
	d2 := scheduler.NewDispatcher(durable, active, nil)
	jobID := dispatchAll(t, d2)
	if active.Created != 1 {
		t.Errorf("replay created a second object: %d", active.Created)
	}
	if got := jobStatus(t, durable, jobID); got != model.JobDispatched {
		t.Errorf("job status = %s, want dispatched", got)
	}
}

// TestWindow3_ObservedProjectionFails: observation arrives, projection to PG
// fails once. Replay with the same observation must be idempotent and the
// run aggregate must reflect the terminal state exactly once.
func TestWindow3_ObservedProjectionFails(t *testing.T) {
	durable, _, ing, _ := newFixture(t)
	if _, _, err := ing.Ingest(context.Background(), pushDelivery("w3")); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	active := memory.NewActiveStore()
	jobID := dispatchAll(t, scheduler.NewDispatcher(durable, active, nil))

	flaky := &failingDurable{inner: durable, failApply: 1}
	p := scheduler.NewProjector(flaky, nil)
	if err := p.Project(context.Background(), jobID, model.PhaseSucceeded); err == nil {
		t.Fatal("expected injected projection failure")
	}

	// Replay the same observation on the healthy store.
	p2 := scheduler.NewProjector(durable, nil)
	for i := 0; i < 3; i++ {
		if err := p2.Project(context.Background(), jobID, model.PhaseSucceeded); err != nil {
			t.Fatalf("replay %d: %v", i, err)
		}
	}
	if got := jobStatus(t, durable, jobID); got != model.JobSucceeded {
		t.Errorf("job status = %s, want succeeded", got)
	}

	// Stale running observation after terminal must not resurrect progress.
	if err := p2.Project(context.Background(), jobID, model.PhaseRunning); err != nil {
		t.Fatalf("stale observation must be accepted: %v", err)
	}
	if got := jobStatus(t, durable, jobID); got != model.JobSucceeded {
		t.Errorf("stale observation changed terminal status to %s", got)
	}

	jobs := mustListJobs(t, durable)
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobs))
	}
	run := mustRun(t, durable)
	if run.Status != model.RunQueued {
		t.Errorf("run with one pending job must stay queued, got %s", run.Status)
	}
}

// TestWindow3_RunAggregateReachesTerminal drives the second job to success
// and asserts the run aggregate flips to succeeded exactly as expected.
func TestWindow3_RunAggregateReachesTerminal(t *testing.T) {
	durable, active, ing, _ := newFixture(t)
	if _, _, err := ing.Ingest(context.Background(), pushDelivery("w3b")); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	p := scheduler.NewProjector(durable, nil)
	d := scheduler.NewDispatcher(durable, active, nil)
	for range standardJobs() {
		jobID := dispatchAll(t, d)
		if err := p.Project(context.Background(), jobID, model.PhaseSucceeded); err != nil {
			t.Fatalf("project: %v", err)
		}
	}
	if run := mustRun(t, durable); run.Status != model.RunSucceeded {
		t.Errorf("run status = %s, want succeeded", run.Status)
	}
}

// TestWindow4_GCAfterTerminalOnly: the active object must not be collected
// before the durable run is terminal, must be collected after, and terminal
// state must survive repeated collection.
func TestWindow4_GCAfterTerminalOnly(t *testing.T) {
	durable, active, ing, _ := newFixture(t)
	if _, _, err := ing.Ingest(context.Background(), pushDelivery("w4")); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	c := scheduler.NewCollector(durable, active, nil)
	d := scheduler.NewDispatcher(durable, active, nil)
	p := scheduler.NewProjector(durable, nil)

	first := dispatchAll(t, d)

	// Not terminal yet: nothing may be collected.
	n, err := c.Collect(context.Background())
	if err != nil || n != 0 {
		t.Fatalf("collect before terminal: n=%d err=%v", n, err)
	}
	if active.DeleteCalls != 0 {
		t.Fatalf("objects deleted before terminal: %d", active.DeleteCalls)
	}

	// Drive only one job terminal: the other keeps the run open.
	if err := p.Project(context.Background(), first, model.PhaseSucceeded); err != nil {
		t.Fatalf("project: %v", err)
	}
	n, err = c.Collect(context.Background())
	if err != nil || n != 0 {
		t.Fatalf("collect with open run: n=%d err=%v", n, err)
	}

	second := dispatchAll(t, d)
	if err := p.Project(context.Background(), second, model.PhaseSucceeded); err != nil {
		t.Fatalf("project: %v", err)
	}

	n, err = c.Collect(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("collect after terminal: n=%d err=%v", n, err)
	}
	if len(active.Objects()) != 0 {
		t.Errorf("objects left after GC: %d", len(active.Objects()))
	}

	// Repeated collection is a no-op (collected jobs are not re-scanned);
	// terminal state is untouched.
	if n, err := c.Collect(context.Background()); err != nil || n != 0 {
		t.Fatalf("second collect: n=%d err=%v", n, err)
	}
	if active.DeleteCalls != 2 {
		t.Errorf("delete calls = %d, want 2 (exactly one per job)", active.DeleteCalls)
	}
	if got := jobStatus(t, durable, first); got != model.JobSucceeded {
		t.Errorf("job status after GC = %s, want succeeded", got)
	}
	if run := mustRun(t, durable); run.Status != model.RunSucceeded {
		t.Errorf("run status after GC = %s, want succeeded", run.Status)
	}
}

func mustFirstJob(t *testing.T, s *memory.DurableStore) model.JobRunID {
	t.Helper()
	jobs := mustListJobs(t, s)
	if len(jobs) == 0 {
		t.Fatal("no jobs")
	}
	return jobs[0].ID
}

func mustListJobs(t *testing.T, s *memory.DurableStore) []model.JobRunRecord {
	t.Helper()
	run := mustRun(t, s)
	jobs, err := s.ListJobRuns(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	return jobs
}

func mustRun(t *testing.T, s *memory.DurableStore) model.RunRecord {
	t.Helper()
	runs := s.Runs()
	if len(runs) != 1 {
		t.Fatalf("expected exactly 1 run, got %d", len(runs))
	}
	return runs[0]
}
