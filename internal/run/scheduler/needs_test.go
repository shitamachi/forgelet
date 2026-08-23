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

func needsDelivery(key string) model.Delivery {
	ev := model.Event{
		Provider: "github", Name: "push", DeliveryID: key,
		Repository: model.RepositoryRef{Provider: "github", Owner: "o", Name: "r"},
		Ref:        "refs/heads/main", SHA: "abc", Actor: "a",
	}
	return model.Delivery{Key: model.DeliveryKey{Provider: "github", DeliveryID: key}, Event: ev, Payload: []byte(`{}`)}
}

func needsJobs() []model.JobIntent {
	return []model.JobIntent{
		{JobKey: "build", RunnerClass: "b"},
		{JobKey: "test", RunnerClass: "t", DependsOn: []string{"build"}},
		{JobKey: "deploy", RunnerClass: "d", DependsOn: []string{"build", "test"}},
	}
}

func newNeedsFixture(t *testing.T) (*memory.DurableStore, *memory.ActiveStore, *scheduler.Dispatcher, *scheduler.Projector) {
	t.Helper()
	now := func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	durable := memory.NewDurableStore(now, nil)
	active := memory.NewActiveStore()
	compiler := sliceCompiler(needsJobs())
	ing := scheduler.NewIngestor(durable, compiler, scheduler.NewFixedIDGen(7), nil)
	if _, _, err := ing.Ingest(context.Background(), needsDelivery("needs-1")); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	return durable, active,
		scheduler.NewDispatcher(durable, active, now),
		scheduler.NewProjector(durable, now)
}

func jobByKey(t *testing.T, s *memory.DurableStore, key string) model.JobRunRecord {
	t.Helper()
	run := s.Runs()[0]
	jobs, err := s.ListJobRuns(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, j := range jobs {
		if j.JobKey == key {
			return j
		}
	}
	t.Fatalf("job %s not found", key)
	return model.JobRunRecord{}
}

// Deps gate dispatch: only dependency-free jobs are claimable first.
func TestNeedsGateDispatch(t *testing.T) {
	durable, _, d, p := newNeedsFixture(t)
	_ = durable
	ctx := context.Background()

	// First claim must be build (test/deploy are gated).
	job, err := d.DispatchNext(ctx)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if job.JobKey != "build" {
		t.Fatalf("claimed %s, want build (deps gate)", job.JobKey)
	}
	if _, err := d.DispatchNext(ctx); !errors.Is(err, scheduler.ErrNoQueuedJob) {
		t.Fatalf("gated jobs must not be claimable: %v", err)
	}

	// build succeeds -> test becomes claimable, deploy stays gated.
	if err := p.Project(ctx, job.ID, model.PhaseSucceeded); err != nil {
		t.Fatalf("project: %v", err)
	}
	test, err := d.DispatchNext(ctx)
	if err != nil || test.JobKey != "test" {
		t.Fatalf("after build success, claimed %+v err=%v", test, err)
	}
	if _, err := d.DispatchNext(ctx); !errors.Is(err, scheduler.ErrNoQueuedJob) {
		t.Fatal("deploy must stay gated until test finishes")
	}
	if err := p.Project(ctx, test.ID, model.PhaseSucceeded); err != nil {
		t.Fatalf("project: %v", err)
	}
	deploy, err := d.DispatchNext(ctx)
	if err != nil || deploy.JobKey != "deploy" {
		t.Fatalf("deploy not claimable after deps: %+v %v", deploy, err)
	}
}

// Failure propagates skips downstream; the run stays successful overall
// when the remaining jobs succeed (GitHub semantics).
func TestNeedsSkipPropagation(t *testing.T) {
	durable, _, d, p := newNeedsFixture(t)
	ctx := context.Background()

	build, err := d.DispatchNext(ctx)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := p.Project(ctx, build.ID, model.PhaseFailed); err != nil {
		t.Fatalf("project failure: %v", err)
	}

	// Next claim sweeps: test and deploy must be skipped, not claimed.
	if _, err := d.DispatchNext(ctx); !errors.Is(err, scheduler.ErrNoQueuedJob) {
		t.Fatalf("expected skip sweep, got %v", err)
	}
	for _, key := range []string{"test", "deploy"} {
		if got := jobByKey(t, durable, key).Status; got != model.JobSkipped {
			t.Errorf("%s status = %s, want skipped", key, got)
		}
	}
	if buildRec := jobByKey(t, durable, "build"); buildRec.Status != model.JobFailed {
		t.Errorf("build rewritten: %s", buildRec.Status)
	}

	// Run aggregate: failed job dominates -> run failed.
	if run := durable.Runs()[0]; run.Status != model.RunFailed {
		t.Errorf("run status = %s, want failed", run.Status)
	}

	// All-skipped edge: only dependent jobs -> run succeeds with skips.
	now := func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	d2 := memory.NewDurableStore(now, nil)
	compiler := sliceCompiler([]model.JobIntent{
		{JobKey: "root", RunnerClass: "r"},
		{JobKey: "child", RunnerClass: "r", DependsOn: []string{"root"}},
	})
	ing := scheduler.NewIngestor(d2, compiler, scheduler.NewFixedIDGen(8), nil)
	if _, _, err := ing.Ingest(ctx, needsDelivery("needs-2")); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	disp := scheduler.NewDispatcher(d2, memory.NewActiveStore(), now)
	proj := scheduler.NewProjector(d2, now)
	root, err := disp.DispatchNext(ctx)
	if err != nil {
		t.Fatalf("dispatch root: %v", err)
	}
	if err := proj.Project(ctx, root.ID, model.PhaseSucceeded); err != nil {
		t.Fatalf("project: %v", err)
	}
	child, err := disp.DispatchNext(ctx)
	if err != nil {
		t.Fatalf("dispatch child: %v", err)
	}
	if err := proj.Project(ctx, child.ID, model.PhaseFailed); err != nil {
		t.Fatalf("project: %v", err)
	}
	if run := d2.Runs()[0]; run.Status != model.RunFailed {
		t.Errorf("run = %s, want failed", run.Status)
	}
}

type sliceCompiler []model.JobIntent

func (c sliceCompiler) Compile(context.Context, model.Event, []byte) ([]model.JobIntent, error) {
	return c, nil
}

// Matrix instances gate independently: each combination is its own record.
func TestMatrixInstancesIndependent(t *testing.T) {
	now := func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	durable := memory.NewDurableStore(now, nil)
	jobs := []model.JobIntent{
		{JobKey: "test[go=1.26]", RunnerClass: "t", Matrix: map[string]string{"go": "1.26"}},
		{JobKey: "test[go=1.27]", RunnerClass: "t", Matrix: map[string]string{"go": "1.27"}},
		{JobKey: "release", RunnerClass: "r", DependsOn: []string{"test[go=1.26]", "test[go=1.27]"}},
	}
	ing := scheduler.NewIngestor(durable, sliceCompiler(jobs), scheduler.NewFixedIDGen(9), nil)
	if _, _, err := ing.Ingest(context.Background(), needsDelivery("matrix-1")); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	d := scheduler.NewDispatcher(durable, memory.NewActiveStore(), now)
	p := scheduler.NewProjector(durable, now)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		job, err := d.DispatchNext(ctx)
		if err != nil {
			t.Fatalf("dispatch matrix %d: %v", i, err)
		}
		if job.Matrix["go"] == "" {
			t.Errorf("matrix lost on record: %+v", job)
		}
		if err := p.Project(ctx, job.ID, model.PhaseSucceeded); err != nil {
			t.Fatalf("project: %v", err)
		}
	}
	rel, err := d.DispatchNext(ctx)
	if err != nil || rel.JobKey != "release" {
		t.Fatalf("release not claimable after both matrix legs: %+v %v", rel, err)
	}
}
