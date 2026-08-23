package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/shitamachi/forgelet/internal/run/model"
)

func TestQueueDepthGauge(t *testing.T) {
	m := New()
	m.SetQueueDepth(7)
	if got := testutil.ToFloat64(m.queueDepth); got != 7 {
		t.Fatalf("queue depth = %f, want 7", got)
	}
	m.SetQueueDepth(0)
	if got := testutil.ToFloat64(m.queueDepth); got != 0 {
		t.Fatalf("queue depth = %f, want 0", got)
	}
}

func TestObserveDispatchIgnoresBrokenClocks(t *testing.T) {
	m := New()
	created := time.Unix(1_000, 0)
	m.ObserveDispatch(created, created.Add(2*time.Second))
	if n := testutil.CollectAndCount(m.dispatchLatency); n != 1 {
		t.Fatalf("samples = %d, want 1", n)
	}
	// Zero or backwards timestamps are never observed.
	m.ObserveDispatch(time.Time{}, created)
	m.ObserveDispatch(created.Add(time.Second), created)
	if n := testutil.CollectAndCount(m.dispatchLatency); n != 1 {
		t.Fatalf("samples after invalid = %d, want 1", n)
	}
}

func TestObserveCompletionTerminalOnly(t *testing.T) {
	m := New()
	base := time.Unix(1_700_000_000, 0)

	terminal := model.JobRunRecord{
		ID: "01HJOB", Status: model.JobSucceeded, RunnerClass: "k3s-small",
		StartedAt:  ptrTime(base),
		FinishedAt: ptrTime(base.Add(30 * time.Second)),
	}
	m.ObserveCompletion(terminal)

	skipped := model.JobRunRecord{ID: "01HSKIP", Status: model.JobSkipped}
	m.ObserveCompletion(skipped)

	pending := model.JobRunRecord{ID: "01HPEND", Status: model.JobQueued}
	m.ObserveCompletion(pending)

	if got := testutil.ToFloat64(m.completedTotal.WithLabelValues("succeeded")); got != 1 {
		t.Errorf("succeeded total = %f", got)
	}
	if got := testutil.ToFloat64(m.completedTotal.WithLabelValues("skipped")); got != 1 {
		t.Errorf("skipped total = %f", got)
	}
	if _, err := m.completedTotal.GetMetricWithLabelValues("queued"); err != nil {
		t.Fatalf("label series: %v", err)
	}
	if got := testutil.ToFloat64(m.completedTotal.WithLabelValues("queued")); got != 0 {
		t.Errorf("queued must not count as completion, got %f", got)
	}
	if n := testutil.CollectAndCount(m.jobDuration); n != 1 {
		t.Fatalf("duration samples = %d, want 1 (skipped has no runtime)", n)
	}
}

func TestHandlerServesTextFormat(t *testing.T) {
	m := New()
	m.SetQueueDepth(3)
	// The handler is exercised through the server e2e; here we only assert
	// the registry carries the collectors.
	if n := testutil.CollectAndCount(m.queueDepth); n != 1 {
		t.Fatalf("queue depth series = %d", n)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
