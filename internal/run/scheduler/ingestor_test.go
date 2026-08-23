package scheduler_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/run/scheduler"
	"github.com/shitamachi/forgelet/internal/storage/memory"
)

func testEvent(delivery string) model.Event {
	return model.Event{
		Provider:   "github",
		Name:       "push",
		DeliveryID: delivery,
		Repository: model.RepositoryRef{Provider: "github", Owner: "shitamachi", Name: "forgelet"},
		Ref:        "refs/heads/main",
		SHA:        "abc123",
		Actor:      "guo",
	}
}

func pushDelivery(delivery string) model.Delivery {
	ev := testEvent(delivery)
	return model.Delivery{
		Key:     model.DeliveryKey{Provider: ev.Provider, DeliveryID: ev.DeliveryID},
		Event:   ev,
		Payload: []byte(`{"zen":"ok"}`),
	}
}

// fakeCompiler returns a fixed job list, optionally failing first.
type fakeCompiler struct {
	mu      sync.Mutex
	fail    error
	calls   int
	jobs    []model.JobIntent
	callsBy map[string]int
}

func newFakeCompiler(jobs ...model.JobIntent) *fakeCompiler {
	return &fakeCompiler{jobs: jobs, callsBy: map[string]int{}}
}

func (c *fakeCompiler) Compile(_ context.Context, ev model.Event, _ []byte) ([]model.JobIntent, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.callsBy[ev.DeliveryID]++
	if c.fail != nil {
		return nil, c.fail
	}
	return c.jobs, nil
}

func (c *fakeCompiler) setFail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fail = err
}

func standardJobs() []model.JobIntent {
	return []model.JobIntent{
		{JobKey: "test", RunnerClass: "k3s-small", PlanDigest: "d1"},
		{JobKey: "build", RunnerClass: "k3s-medium", PlanDigest: "d2"},
	}
}

func newFixture(t *testing.T, jobs ...model.JobIntent) (*memory.DurableStore, *memory.ActiveStore, *scheduler.Ingestor, *fakeCompiler) {
	t.Helper()
	if len(jobs) == 0 {
		jobs = standardJobs()
	}
	durable := memory.NewDurableStore(func() time.Time { return time.Unix(0, 0).UTC() }, nil)
	active := memory.NewActiveStore()
	compiler := newFakeCompiler(jobs...)
	ids := scheduler.NewFixedIDGen(1_700_000_000_000)
	ing := scheduler.NewIngestor(durable, compiler, ids, nil)
	return durable, active, ing, compiler
}

func TestIngestorDeduplicatesReplayedDelivery(t *testing.T) {
	durable, _, ing, compiler := newFixture(t)

	d := pushDelivery("d-1")
	runID1, created1, err := ing.Ingest(context.Background(), d)
	if err != nil || !created1 {
		t.Fatalf("first ingest: created=%v err=%v", created1, err)
	}
	for i := 0; i < 5; i++ {
		runIDn, createdn, err := ing.Ingest(context.Background(), d)
		if err != nil {
			t.Fatalf("replay %d: %v", i, err)
		}
		if createdn {
			t.Fatalf("replay %d must not create a new run", i)
		}
		if runIDn != runID1 {
			t.Fatalf("replay %d returned run %s, want %s", i, runIDn, runID1)
		}
	}
	if compiler.calls != 1 {
		t.Errorf("compiler ran %d times, want 1 (dedupe precedes expensive work)", compiler.calls)
	}
	jobs, err := durable.ListJobRuns(context.Background(), runID1)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs) != len(standardJobs()) {
		t.Errorf("run has %d jobs, want %d", len(jobs), len(standardJobs()))
	}
}

func TestIngestorCompileFailureKeepsDeliveryReceipt(t *testing.T) {
	durable, _, ing, compiler := newFixture(t)
	compiler.setFail(errors.New("boom"))

	d := pushDelivery("d-2")
	if _, _, err := ing.Ingest(context.Background(), d); err == nil {
		t.Fatal("compile failure must surface")
	}

	rec, created, err := durable.RecordDelivery(context.Background(), d)
	if err != nil || created {
		t.Fatalf("delivery receipt missing after compile failure: rec=%+v created=%v err=%v", rec, created, err)
	}
	if rec.RunID != "" {
		t.Fatalf("delivery bound to run %s despite compile failure", rec.RunID)
	}

	// Recovery: same delivery replay now compiles and creates exactly one run.
	compiler.setFail(nil)
	runID, created, err := ing.Ingest(context.Background(), d)
	if err != nil || !created {
		t.Fatalf("replay after fix: created=%v err=%v", created, err)
	}
	if _, _, err := ing.Ingest(context.Background(), d); err != nil {
		t.Fatalf("second replay: %v", err)
	}
	jobs, err := durable.ListJobRuns(context.Background(), runID)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs) != 2 {
		t.Errorf("expected 2 jobs after recovery, got %d", len(jobs))
	}
}

func TestIngestorRejectsInvalidJobIntent(t *testing.T) {
	_, _, ing, _ := newFixture(t, model.JobIntent{JobKey: "", RunnerClass: "x"})
	if _, _, err := ing.Ingest(context.Background(), pushDelivery("d-3")); err == nil {
		t.Fatal("invalid intent must fail ingestion")
	}
}
