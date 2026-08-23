package memory

import (
	"context"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/run/scheduler"
)

func fixedClock() Clock {
	return func() time.Time { return time.Unix(0, 0).UTC() }
}

func delivery(key string) model.Delivery {
	ev := model.Event{
		Provider: "github", Name: "push", DeliveryID: key,
		Repository: model.RepositoryRef{Provider: "github", Owner: "o", Name: "r"},
		Ref:        "refs/heads/main", SHA: "abc", Actor: "a",
	}
	return model.Delivery{
		Key:     model.DeliveryKey{Provider: "github", DeliveryID: key},
		Event:   ev,
		Payload: []byte(`{}`),
	}
}

func seed(key string, jobs ...model.JobIntent) model.RunSeed {
	d := delivery(key)
	return model.RunSeed{Delivery: d.Key, Event: d.Event, Jobs: jobs}
}

// recordedSeed records the delivery first, mirroring the ingest protocol.
func recordedSeed(t *testing.T, s *DurableStore, key string, jobs ...model.JobIntent) model.RunSeed {
	t.Helper()
	if _, _, err := s.RecordDelivery(context.Background(), delivery(key)); err != nil {
		t.Fatalf("record delivery %s: %v", key, err)
	}
	return seed(key, jobs...)
}

func intents(n int) []model.JobIntent {
	out := make([]model.JobIntent, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, model.JobIntent{JobKey: "job-" + string(rune('a'+i)), RunnerClass: "small"})
	}
	return out
}

func TestRecordDeliveryFirstWriteWins(t *testing.T) {
	s := NewDurableStore(fixedClock(), nil)
	ctx := context.Background()

	rec1, created1, err := s.RecordDelivery(ctx, delivery("d1"))
	if err != nil || !created1 {
		t.Fatalf("first record: created=%v err=%v", created1, err)
	}

	// A second delivery with the same key must not overwrite the stored payload.
	dup := delivery("d1")
	dup.Payload = []byte(`{"tampered":true}`)
	rec2, created2, err := s.RecordDelivery(ctx, dup)
	if err != nil || created2 {
		t.Fatalf("duplicate record: created=%v err=%v", created2, err)
	}
	if string(rec2.Payload) != string(rec1.Payload) {
		t.Error("duplicate delivery overwrote the original payload")
	}
}

func TestCreateRunRequiresRecordedDelivery(t *testing.T) {
	s := NewDurableStore(fixedClock(), nil)
	if _, _, err := s.CreateRun(context.Background(), seed("ghost"), time.Unix(0, 0)); err == nil {
		t.Fatal("create run for unrecorded delivery must fail")
	}
}

func TestCreateRunAtomicOnDuplicateJobKey(t *testing.T) {
	s := NewDurableStore(fixedClock(), nil)
	ctx := context.Background()
	sd := recordedSeed(t, s, "dup", model.JobIntent{JobKey: "test", RunnerClass: "small"}, model.JobIntent{JobKey: "test", RunnerClass: "small"})
	if _, _, err := s.CreateRun(ctx, sd, time.Unix(0, 0)); err == nil {
		t.Fatal("duplicate job key must fail")
	}
	// Failure must not leave partial state: no run, delivery unbound.
	if runs := s.Runs(); len(runs) != 0 {
		t.Fatalf("partial run leaked after error: %d", len(runs))
	}
	rec, _, err := s.RecordDelivery(ctx, delivery("dup"))
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if rec.RunID != "" {
		t.Fatalf("delivery bound to leaked run %s", rec.RunID)
	}
}

func TestCreateRunCreateOrGet(t *testing.T) {
	s := NewDurableStore(fixedClock(), nil)
	ctx := context.Background()
	sd := recordedSeed(t, s, "cog", intents(2)...)

	run1, created1, err := s.CreateRun(ctx, sd, time.Unix(0, 0))
	if err != nil || !created1 {
		t.Fatalf("first create: created=%v err=%v", created1, err)
	}
	run2, created2, err := s.CreateRun(ctx, sd, time.Unix(1, 0))
	if err != nil || created2 {
		t.Fatalf("second create: created=%v err=%v", created2, err)
	}
	if run1.ID != run2.ID {
		t.Errorf("replay minted a new run: %s vs %s", run1.ID, run2.ID)
	}
	jobs, err := s.ListJobRuns(ctx, run1.ID)
	if err != nil || len(jobs) != 2 {
		t.Fatalf("jobs=%d err=%v, want 2", len(jobs), err)
	}
}

func TestClaimSerializedAndReleasedByAck(t *testing.T) {
	s := NewDurableStore(fixedClock(), nil)
	ctx := context.Background()
	if _, _, err := s.CreateRun(ctx, recordedSeed(t, s, "claim", intents(2)...), time.Unix(0, 0)); err != nil {
		t.Fatalf("create: %v", err)
	}

	j1, err := s.ClaimNextQueuedJob(ctx)
	if err != nil {
		t.Fatalf("claim 1: %v", err)
	}
	j2, err := s.ClaimNextQueuedJob(ctx)
	if err != nil {
		t.Fatalf("claim 2: %v", err)
	}
	if j1.ID == j2.ID {
		t.Fatalf("same job %s claimed twice", j1.ID)
	}
	if _, err := s.ClaimNextQueuedJob(ctx); err == nil {
		t.Fatal("expected ErrNoQueuedJob while both claims are held")
	}

	// Ack releases the claim; the job is dispatched, not re-claimable.
	if err := s.AckDispatched(ctx, j1.ID, scheduler.ActiveObject{Name: j1.ID.CRName(), UID: "u1"}, time.Unix(1, 0)); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if _, err := s.ClaimNextQueuedJob(ctx); err == nil {
		t.Fatal("dispatched job must not be claimable")
	}

	// Releasing a held claim makes the job claimable again (W1 recovery).
	if err := s.ReleaseClaim(ctx, j2.ID); err != nil {
		t.Fatalf("release: %v", err)
	}
	again, err := s.ClaimNextQueuedJob(ctx)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if again.ID != j2.ID {
		t.Fatalf("reclaim returned %s, want %s", again.ID, j2.ID)
	}
}

func TestAckDispatchedGuards(t *testing.T) {
	s := NewDurableStore(fixedClock(), nil)
	ctx := context.Background()
	if _, _, err := s.CreateRun(ctx, recordedSeed(t, s, "ack", intents(1)...), time.Unix(0, 0)); err != nil {
		t.Fatalf("create: %v", err)
	}
	job, err := s.ClaimNextQueuedJob(ctx)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	obj := scheduler.ActiveObject{Name: job.ID.CRName(), UID: "u"}
	if err := s.AckDispatched(ctx, job.ID, obj, time.Unix(1, 0)); err != nil {
		t.Fatalf("ack: %v", err)
	}
	// Idempotent replay with the same object name (possibly new UID).
	obj2 := scheduler.ActiveObject{Name: job.ID.CRName(), UID: "u2"}
	if err := s.AckDispatched(ctx, job.ID, obj2, time.Unix(2, 0)); err != nil {
		t.Fatalf("ack replay: %v", err)
	}
	got, err := s.GetJobRun(ctx, job.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ActiveUID != "u2" || got.ActiveName != job.ID.CRName() {
		t.Errorf("ack replay did not refresh object: %+v", got)
	}

	// A different object name for the same job is refused.
	if err := s.AckDispatched(ctx, job.ID, scheduler.ActiveObject{Name: "other", UID: "x"}, time.Unix(3, 0)); err == nil {
		t.Fatal("conflicting object name must be refused")
	}

	// Unknown job.
	if err := s.AckDispatched(ctx, model.JobRunID("zzz"), obj, time.Unix(0, 0)); err == nil {
		t.Fatal("unknown job must fail")
	}

	// Drive to terminal, then ack must be refused.
	if err := s.ApplyObserved(ctx, job.ID, model.PhaseSucceeded, time.Unix(4, 0)); err != nil {
		t.Fatalf("observe: %v", err)
	}
	if err := s.AckDispatched(ctx, job.ID, obj, time.Unix(5, 0)); err == nil {
		t.Fatal("ack on terminal job must fail")
	}
}

func TestApplyObservedMonotonic(t *testing.T) {
	s := NewDurableStore(fixedClock(), nil)
	ctx := context.Background()
	if _, _, err := s.CreateRun(ctx, recordedSeed(t, s, "obs", intents(2)...), time.Unix(0, 0)); err != nil {
		t.Fatalf("create: %v", err)
	}
	jobs, err := s.ListJobRuns(ctx, s.Runs()[0].ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	a, b := jobs[0], jobs[1]

	// Observation before dispatch jumps straight to running (rank rises).
	if err := s.ApplyObserved(ctx, a.ID, model.PhaseRunning, time.Unix(1, 0)); err != nil {
		t.Fatalf("observe running: %v", err)
	}
	if got, _ := s.GetJobRun(ctx, a.ID); got.Status != model.JobRunning {
		t.Errorf("status = %s, want running", got.Status)
	}

	// Stale pending is a no-op.
	if err := s.ApplyObserved(ctx, a.ID, model.PhasePending, time.Unix(2, 0)); err != nil {
		t.Fatalf("pending: %v", err)
	}
	if got, _ := s.GetJobRun(ctx, a.ID); got.Status != model.JobRunning {
		t.Errorf("pending changed status to %s", got.Status)
	}

	// Terminal sticks.
	if err := s.ApplyObserved(ctx, a.ID, model.PhaseFailed, time.Unix(3, 0)); err != nil {
		t.Fatalf("failed: %v", err)
	}
	if err := s.ApplyObserved(ctx, a.ID, model.PhaseSucceeded, time.Unix(4, 0)); err != nil {
		t.Fatalf("succeeded after failed: %v", err)
	}
	if got, _ := s.GetJobRun(ctx, a.ID); got.Status != model.JobFailed {
		t.Errorf("terminal switched to %s", got.Status)
	}
	if got, _ := s.GetJobRun(ctx, a.ID); got.FinishedAt == nil {
		t.Error("terminal observation must set FinishedAt")
	}

	// Unknown job errors.
	if err := s.ApplyObserved(ctx, model.JobRunID("zzz"), model.PhaseRunning, time.Unix(0, 0)); err == nil {
		t.Fatal("unknown job must fail")
	}
	_ = b
}

func TestListGCReadyFiltersCollected(t *testing.T) {
	s := NewDurableStore(fixedClock(), nil)
	ctx := context.Background()
	if _, _, err := s.CreateRun(ctx, recordedSeed(t, s, "gc", intents(2)...), time.Unix(0, 0)); err != nil {
		t.Fatalf("create: %v", err)
	}
	runID := s.Runs()[0].ID
	jobs, err := s.ListJobRuns(ctx, runID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, j := range jobs {
		if err := s.AckDispatched(ctx, j.ID, scheduler.ActiveObject{Name: j.ID.CRName()}, time.Unix(1, 0)); err != nil {
			t.Fatalf("ack: %v", err)
		}
		if err := s.ApplyObserved(ctx, j.ID, model.PhaseSucceeded, time.Unix(2, 0)); err != nil {
			t.Fatalf("observe: %v", err)
		}
	}

	ready, err := s.ListGCReadyJobs(ctx)
	if err != nil || len(ready) != 2 {
		t.Fatalf("ready=%d err=%v, want 2", len(ready), err)
	}
	if err := s.MarkCollected(ctx, ready[0].ID, time.Unix(3, 0)); err != nil {
		t.Fatalf("mark: %v", err)
	}
	ready, err = s.ListGCReadyJobs(ctx)
	if err != nil || len(ready) != 1 {
		t.Fatalf("after mark ready=%d err=%v, want 1", len(ready), err)
	}
	// Marking twice is a no-op.
	if err := s.MarkCollected(ctx, ready[0].ID, time.Unix(4, 0)); err != nil {
		t.Fatalf("mark: %v", err)
	}
}

func TestCancelRunLeavesTerminalJobs(t *testing.T) {
	s := NewDurableStore(fixedClock(), nil)
	ctx := context.Background()
	if _, _, err := s.CreateRun(ctx, recordedSeed(t, s, "cx", intents(2)...), time.Unix(0, 0)); err != nil {
		t.Fatalf("create: %v", err)
	}
	runID := s.Runs()[0].ID
	jobs, err := s.ListJobRuns(ctx, runID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	// One job terminal-succeeded before cancellation.
	if err := s.AckDispatched(ctx, jobs[0].ID, scheduler.ActiveObject{Name: jobs[0].ID.CRName()}, time.Unix(1, 0)); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if err := s.ApplyObserved(ctx, jobs[0].ID, model.PhaseSucceeded, time.Unix(2, 0)); err != nil {
		t.Fatalf("observe: %v", err)
	}

	if err := s.CancelRun(ctx, runID, time.Unix(3, 0)); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	a, _ := s.GetJobRun(ctx, jobs[0].ID)
	b, _ := s.GetJobRun(ctx, jobs[1].ID)
	if a.Status != model.JobSucceeded {
		t.Errorf("terminal job rewritten to %s", a.Status)
	}
	if b.Status != model.JobCancelled {
		t.Errorf("open job status = %s, want cancelled", b.Status)
	}
	run, err := s.GetRun(ctx, runID)
	if err != nil || run.Status != model.RunCancelled {
		t.Fatalf("run status = %s err=%v, want cancelled", run.Status, err)
	}

	// Cancel is idempotent for an already-cancelled run...
	if err := s.CancelRun(ctx, runID, time.Unix(4, 0)); err != nil {
		t.Fatalf("recancel: %v", err)
	}
	// ...but refused for other terminal states.
	if err := s.CancelRun(ctx, model.RunID("zzz"), time.Unix(0, 0)); err == nil {
		t.Fatal("unknown run must fail")
	}
}

func TestActiveStoreCreateOrGetDeterministic(t *testing.T) {
	a := NewActiveStore()
	ctx := context.Background()
	id := model.JobRunID("01JTEST00000000000000000XYZ")

	obj1, err := a.CreateOrGet(ctx, id)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	obj2, err := a.CreateOrGet(ctx, id)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if obj1 != obj2 || obj1.Name != id.CRName() {
		t.Errorf("create-or-get not deterministic: %+v vs %+v", obj1, obj2)
	}
	if a.Created != 1 || a.CreateCalls != 2 {
		t.Errorf("Created=%d CreateCalls=%d, want 1/2", a.Created, a.CreateCalls)
	}

	// Delete is idempotent.
	if err := a.Delete(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := a.Delete(ctx, id); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
	if len(a.Objects()) != 0 {
		t.Error("object not removed")
	}
}
