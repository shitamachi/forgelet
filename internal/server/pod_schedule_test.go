package server

import (
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/shitamachi/forgelet/internal/run"
	"github.com/shitamachi/forgelet/internal/storage"
)

func TestPendingPodScheduleTimeoutAcrossRestart(t *testing.T) {
	h := newHarness(t, 1)
	item := h.manual(testWorkflow)
	next := h.manual(testWorkflow)
	h.tick()
	job := podScheduleRun(h, item.ID).Jobs[0]
	pod := h.runtime.pods[job.ID]
	pod.phase = corev1.PodPending
	pod.observation.CreatedAt = job.ReservedAt.Add(10 * time.Second)
	pod.observation.SchedulingReason = "Unschedulable 0/1 nodes: Insufficient cpu"
	h.runtime.pods[job.ID] = pod
	expires := pod.observation.CreatedAt.Add(5 * time.Minute)
	h.server.now = func() time.Time { return expires.Add(-time.Second) }
	h.tick()
	if job := podScheduleRun(h, item.ID).Jobs[0]; job.State != run.Starting {
		t.Fatalf("expired before the Pod's scheduling deadline: %+v", job)
	}

	h.http.Close()
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(t.Context(), h.path)
	if err != nil {
		t.Fatal(err)
	}
	h.store = db
	h.start()
	h.server.now = func() time.Time { return expires }
	// Bound but still pulling an image is distinct from unscheduled.
	pod.observation.Scheduled = true
	h.runtime.pods[job.ID] = pod
	h.tick()
	if got := podScheduleRun(h, item.ID).Jobs[0].State; got != run.Starting {
		t.Fatalf("bound Pending Pod changed state to %s", got)
	}
	pod.observation.Scheduled = false
	h.runtime.pods[job.ID] = pod
	h.tick()
	got := podScheduleRun(h, item.ID).Jobs[0]
	if got.State != run.Interrupted || got.Result == nil || !strings.Contains(got.Result.Error, "Unschedulable") {
		t.Fatalf("missing timeout diagnostic: %+v", got)
	}
	if _, exists := h.runtime.pods[job.ID]; exists || podScheduleRun(h, next.ID).Jobs[0].State != run.Starting {
		t.Fatal("Pod was not removed and its slot reused")
	}
	if _, err := h.store.Claim(t.Context(), job.ID, job.PodUID); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("late claim after timeout: %v", err)
	}
}

func TestPodScheduleTimeoutCannotOverrideClaim(t *testing.T) {
	h := newHarness(t, 1)
	item := h.manual(testWorkflow)
	h.tick()
	job := podScheduleRun(h, item.ID).Jobs[0]
	if _, err := h.store.Claim(t.Context(), job.ID, job.PodUID); err != nil {
		t.Fatal(err)
	}
	expired, err := h.store.InterruptUnscheduledPod(t.Context(), job.ID, job.PodUID, "stale scheduling observation")
	if err != nil || expired || podScheduleRun(h, item.ID).Jobs[0].State != run.Running {
		t.Fatalf("stale observation overwrote a claim: %t, %v", expired, err)
	}
}

func podScheduleRun(h *harness, id string) run.Run {
	h.t.Helper()
	item, err := h.store.GetRun(h.t.Context(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return item
}
