package storage

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/delivery"
)

func TestDeliveryPersistenceCASAndRestoreQuarantine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forgelet.db")
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	r := delivery.Record{ID: "one", RunID: "run", JobID: "job", Target: delivery.Target{Name: "app"}, Digest: "digest", State: delivery.Queued, CreatedAt: now, UpdatedAt: now}
	r, err = s.EnqueueDelivery(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueueDelivery(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	changed := r
	changed.Digest = "different"
	if _, err := s.EnqueueDelivery(t.Context(), changed); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	r.State = delivery.Preparing
	if err := s.SaveDelivery(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveDelivery(t.Context(), r); !errors.Is(err, ErrConflict) {
		t.Fatal("stale update accepted", err)
	}
	backup := filepath.Join(t.TempDir(), "restored.db")
	if err := s.Backup(t.Context(), backup); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	saved, err := s.GetDelivery(t.Context(), r.ID)
	if err != nil || saved.State != delivery.Preparing || saved.Version != 2 {
		t.Fatal(saved, err)
	}
	if err := PrepareRestore(t.Context(), backup); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(t.Context(), backup)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	saved, err = restored.GetDelivery(t.Context(), r.ID)
	if err != nil || saved.State != delivery.Cancelled || saved.Error == "" {
		t.Fatal("restore replayed remote writes", saved, err)
	}
}

func TestDeliveryRetentionProtectsQueuedRunAndLatestObservation(t *testing.T) {
	s := openTest(t)
	now := time.Now()
	old := now.Add(-40 * 24 * time.Hour)
	r, job := oldFinishedRun(t, s, "delivery", 0, old)
	queued, err := s.EnqueueDelivery(t.Context(), delivery.Record{ID: "waiting", RunID: r.ID, JobID: job.JobID, Target: delivery.Target{Name: "app"}, State: delivery.Queued, CreatedAt: old, UpdatedAt: old})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := s.Prune(t.Context(), now, testRetention, map[string]string{}); err != nil || result.Runs != 0 {
		t.Fatal("queued run lost", result, err)
	}
	queued.State = delivery.Deployed
	if err := s.SaveDelivery(t.Context(), queued); err != nil {
		t.Fatal(err)
	}
	if result, err := s.Prune(t.Context(), now, testRetention, map[string]string{}); err != nil || result.Runs != 1 || result.Deployments != 0 {
		t.Fatal("latest deployment lost", result, err)
	}
	queued, err = s.GetDelivery(t.Context(), queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	queued.State = delivery.Superseded
	if err := s.SaveDelivery(t.Context(), queued); err != nil {
		t.Fatal(err)
	}
	if result, err := s.Prune(t.Context(), now, testRetention, map[string]string{}); err != nil || result.Deployments != 1 {
		t.Fatal("old deployment retained", result, err)
	}
	if _, err := s.GetDelivery(t.Context(), queued.ID); !errors.Is(err, delivery.ErrNotFound) {
		t.Fatal(err)
	}
}
