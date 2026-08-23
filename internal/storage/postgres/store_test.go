package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/run/scheduler"
)

// The suite runs only with FORGELET_TEST_POSTGRES set (CI provides the
// service; locally: docker run -e POSTGRES_PASSWORD=test -p 55432:5432 postgres:16-alpine).
func testDatabase(t *testing.T) (*Store, bool) {
	t.Helper()
	url := os.Getenv("FORGELET_TEST_POSTGRES")
	if url == "" {
		t.Skip("FORGELET_TEST_POSTGRES not set; skipping PostgreSQL integration")
		return nil, false
	}
	s, err := New(context.Background(), url, scheduler.NewFixedIDGen(1_700_000_000), func() time.Time {
		return time.Unix(1_700_000_000, 0).UTC()
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// Fresh schema per test.
	if _, err := s.pool.Exec(context.Background(), `TRUNCATE job_runs, workflow_runs, webhook_deliveries`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	t.Cleanup(s.Close)
	return s, true
}

func pgDelivery(key string) model.Delivery {
	ev := model.Event{
		Provider: "github", Name: "push", DeliveryID: key,
		Repository: model.RepositoryRef{Provider: "github", Owner: "o", Name: "r"},
		Ref:        "refs/heads/main", SHA: "abc", Actor: "a",
	}
	return model.Delivery{Key: model.DeliveryKey{Provider: "github", DeliveryID: key}, Event: ev, Payload: []byte(`{"zen":1}`)}
}

func pgJobs() []model.JobIntent {
	return []model.JobIntent{
		{JobKey: "build", RunnerClass: "b"},
		{JobKey: "test", RunnerClass: "t", DependsOn: []string{"build"}, Matrix: map[string]string{"go": "1.27"}},
	}
}

func ingest(t *testing.T, s *Store, key string) model.RunID {
	t.Helper()
	ctx := context.Background()
	if _, _, err := s.RecordDelivery(ctx, pgDelivery(key)); err != nil {
		t.Fatalf("record: %v", err)
	}
	seed := model.RunSeed{Delivery: pgDelivery(key).Key, Event: pgDelivery(key).Event, Jobs: pgJobs()}
	run, _, err := s.CreateRun(ctx, seed, time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	return run.ID
}

func TestPGDeliveryDedupeAndPayload(t *testing.T) {
	s, _ := testDatabase(t)
	ctx := context.Background()

	rec1, created1, err := s.RecordDelivery(ctx, pgDelivery("d1"))
	if err != nil || !created1 {
		t.Fatalf("first: %v %v", created1, err)
	}
	rec2, created2, err := s.RecordDelivery(ctx, pgDelivery("d1"))
	if err != nil || created2 || rec2.RunID != rec1.RunID {
		t.Fatalf("replay: %v %v %+v", created2, err, rec2)
	}
	if string(rec2.Payload) != `{"zen":1}` {
		t.Error("payload not preserved byte-for-byte")
	}
}

func TestPGCreateRunIdempotent(t *testing.T) {
	s, _ := testDatabase(t)
	ctx := context.Background()
	d := pgDelivery("d2")
	if _, _, err := s.RecordDelivery(ctx, d); err != nil {
		t.Fatal(err)
	}
	seed := model.RunSeed{Delivery: d.Key, Event: d.Event, Jobs: pgJobs()}
	run1, c1, err := s.CreateRun(ctx, seed, time.Unix(0, 0).UTC())
	if err != nil || !c1 {
		t.Fatalf("create: %v %v", c1, err)
	}
	run2, c2, err := s.CreateRun(ctx, seed, time.Unix(1, 0).UTC())
	if err != nil || c2 || run1.ID != run2.ID {
		t.Fatalf("replay: %v %v %s vs %s", c2, err, run1.ID, run2.ID)
	}
	jobs, err := s.ListJobRuns(ctx, run1.ID)
	if err != nil || len(jobs) != 2 {
		t.Fatalf("jobs=%d err=%v", len(jobs), err)
	}
	if jobs[1].DependsOn[0] != "build" || jobs[1].Matrix["go"] != "1.27" {
		t.Errorf("depends_on/matrix lost: %+v", jobs[1])
	}
	// Delivery bound.
	rec, _, err := s.RecordDelivery(ctx, d)
	if err != nil || rec.RunID != run1.ID {
		t.Fatalf("binding: %+v %v", rec, err)
	}
}

func TestPGCountQueuedJobs(t *testing.T) {
	s, _ := testDatabase(t)
	_ = ingest(t, s, "dq")
	ctx := context.Background()

	if n, err := s.CountQueuedJobs(ctx); err != nil || n != 2 {
		t.Fatalf("queued after create = %d err=%v, want 2", n, err)
	}
	job, err := s.ClaimNextQueuedJob(ctx)
	if err != nil || job.JobKey != "build" {
		t.Fatalf("claim: %+v %v", job, err)
	}
	// A claim without ack keeps the queued count unchanged.
	if n, err := s.CountQueuedJobs(ctx); err != nil || n != 2 {
		t.Fatalf("queued during claim = %d err=%v, want 2", n, err)
	}
	if err := s.AckDispatched(ctx, job.ID, scheduler.ActiveObject{Name: job.ID.CRName(), UID: "u"}, time.Unix(1, 0).UTC()); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if n, err := s.CountQueuedJobs(ctx); err != nil || n != 1 {
		t.Fatalf("queued after ack = %d err=%v, want 1", n, err)
	}
}

func TestPGClaimGatingAndSkip(t *testing.T) {
	s, _ := testDatabase(t)
	runID := ingest(t, s, "d3")
	ctx := context.Background()

	// build first; test gated.
	job, err := s.ClaimNextQueuedJob(ctx)
	if err != nil || job.JobKey != "build" {
		t.Fatalf("claim: %+v %v", job, err)
	}
	if _, err := s.ClaimNextQueuedJob(ctx); !errors.Is(err, scheduler.ErrNoQueuedJob) {
		t.Fatalf("gated claim must fail: %v", err)
	}
	if err := s.AckDispatched(ctx, job.ID, scheduler.ActiveObject{Name: job.ID.CRName(), UID: "u"}, time.Unix(1, 0).UTC()); err != nil {
		t.Fatalf("ack: %v", err)
	}
	// Idempotent re-ack.
	if err := s.AckDispatched(ctx, job.ID, scheduler.ActiveObject{Name: job.ID.CRName(), UID: "u2"}, time.Unix(2, 0).UTC()); err != nil {
		t.Fatalf("re-ack: %v", err)
	}

	// build fails -> test swept to skipped on next claim.
	if err := s.ApplyObserved(ctx, job.ID, model.PhaseFailed, time.Unix(3, 0).UTC()); err != nil {
		t.Fatalf("observe: %v", err)
	}
	if _, err := s.ClaimNextQueuedJob(ctx); !errors.Is(err, scheduler.ErrNoQueuedJob) {
		t.Fatalf("sweep: %v", err)
	}
	test, err := s.jobByKey(t, runID, "test")
	if err != nil || test.Status != model.JobSkipped {
		t.Fatalf("dependent not skipped: %+v %v", test, err)
	}
	run, err := s.GetRun(ctx, runID)
	if err != nil || run.Status != model.RunFailed {
		t.Fatalf("run = %s %v, want failed", run.Status, err)
	}

	// GC ready + mark + idempotent.
	ready, err := s.ListGCReadyJobs(ctx)
	if err != nil || len(ready) != 2 {
		t.Fatalf("gc ready = %d %v", len(ready), err)
	}
	for _, j := range ready {
		if err := s.MarkCollected(ctx, j.ID, time.Unix(9, 0).UTC()); err != nil {
			t.Fatal(err)
		}
	}
	ready, _ = s.ListGCReadyJobs(ctx)
	if len(ready) != 0 {
		t.Fatalf("after mark: %d", len(ready))
	}
}

func (s *Store) jobByKey(t *testing.T, run model.RunID, key string) (model.JobRunRecord, error) {
	t.Helper()
	jobs, err := s.ListJobRuns(context.Background(), run)
	if err != nil {
		return model.JobRunRecord{}, err
	}
	for _, j := range jobs {
		if j.JobKey == key {
			return j, nil
		}
	}
	return model.JobRunRecord{}, fmt.Errorf("job %s not found", key)
}

func TestPGApplyObservedMonotonic(t *testing.T) {
	s, _ := testDatabase(t)
	runID := ingest(t, s, "d4")
	ctx := context.Background()

	build, err := s.jobByKey(t, runID, "build")
	if err != nil {
		t.Fatal(err)
	}
	// Observation before ack jumps to running.
	if err := s.ApplyObserved(ctx, build.ID, model.PhaseRunning, time.Unix(1, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.jobByKey(t, runID, "build"); got.Status != model.JobRunning {
		t.Fatalf("status = %s", got.Status)
	}
	// Stale pending no-op.
	if err := s.ApplyObserved(ctx, build.ID, model.PhasePending, time.Unix(2, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	// Terminal sticks.
	if err := s.ApplyObserved(ctx, build.ID, model.PhaseSucceeded, time.Unix(3, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyObserved(ctx, build.ID, model.PhaseFailed, time.Unix(4, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	got, _ := s.jobByKey(t, runID, "build")
	if got.Status != model.JobSucceeded || got.StartedAt == nil || got.FinishedAt == nil {
		t.Fatalf("terminal state: %+v", got)
	}

	// Dependent now claimable; ack then cancel run idempotency.
	test, err := s.jobByKey(t, runID, "test")
	if err != nil {
		t.Fatal(err)
	}
	// Release claim race guard.
	if err := s.ReleaseClaim(ctx, test.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.CancelRun(ctx, runID, time.Unix(5, 0).UTC()); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := s.CancelRun(ctx, runID, time.Unix(6, 0).UTC()); err != nil {
		t.Fatalf("recancel must be no-op: %v", err)
	}
	run, _ := s.GetRun(ctx, runID)
	if run.Status != model.RunCancelled {
		t.Fatalf("run = %s", run.Status)
	}
}

func TestPGConcurrentClaimsExclusive(t *testing.T) {
	s, _ := testDatabase(t)
	ingest(t, s, "d5")
	ctx := context.Background()

	const workers = 4
	var mu sync.Mutex
	claimed := map[string]bool{}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job, err := s.ClaimNextQueuedJob(ctx)
			if errors.Is(err, scheduler.ErrNoQueuedJob) {
				return
			}
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}
			mu.Lock()
			if claimed[job.JobKey] {
				t.Errorf("job %s claimed twice", job.JobKey)
			}
			claimed[job.JobKey] = true
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(claimed) != 1 { // build only; test gated
		t.Fatalf("distinct claims = %d, want 1: %v", len(claimed), claimed)
	}
}

func TestPGUnknownIDs(t *testing.T) {
	s, _ := testDatabase(t)
	ctx := context.Background()
	if _, err := s.GetRun(ctx, "nope"); !errors.Is(err, scheduler.ErrRunNotFound) {
		t.Errorf("run: %v", err)
	}
	if _, err := s.GetJobRun(ctx, "nope"); !errors.Is(err, scheduler.ErrJobRunNotFound) {
		t.Errorf("job: %v", err)
	}
	if err := s.AckDispatched(ctx, "nope", scheduler.ActiveObject{}, time.Unix(0, 0).UTC()); !errors.Is(err, scheduler.ErrJobRunNotFound) {
		t.Errorf("ack unknown: %v", err)
	}
}
