package storage

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestReserveExternalHonoursDeviceLabels(t *testing.T) {
	ctx := t.Context()
	store := openTest(t)
	mac := run.Run{ID: "mac-run", DeliveryID: "mac-delivery", Repository: "me/repo", WorkflowPath: ".github/workflows/mac.yml"}
	wslc := run.Run{ID: "wslc-run", DeliveryID: "wslc-delivery", Repository: "me/repo", WorkflowPath: ".github/workflows/linux.yml"}
	if created, err := store.CreateRun(ctx, mac, []run.Plan{{JobID: "mac-job", RunID: mac.ID, Repository: mac.Repository,
		Profile: run.MacRunnerLabel, SourceWorkflow: mac.WorkflowPath, Ref: "refs/heads/main", External: true}}); err != nil || !created {
		t.Fatal(created, err)
	}
	if created, err := store.CreateRun(ctx, wslc, []run.Plan{{JobID: "wslc-job", RunID: wslc.ID, Repository: wslc.Repository,
		Profile: run.WSLCRunnerLabel, SourceWorkflow: wslc.WorkflowPath, Ref: "refs/heads/main", External: true}}); err != nil || !created {
		t.Fatal(created, err)
	}
	repos := map[string]bool{"me/repo": true}
	// A Windows device must not claim the macOS job, even though both share the
	// repository queue.
	job, err := store.ReserveExternal(ctx, 2, ExternalReservation{RunnerID: "win", AttemptID: "win-attempt",
		Repositories: repos, Labels: []string{run.WSLCRunnerLabel}})
	if err != nil || job.ID != "wslc-job" {
		t.Fatalf("windows reservation = %q, %v", job.ID, err)
	}
	job, err = store.ReserveExternal(ctx, 2, ExternalReservation{RunnerID: "mac", AttemptID: "mac-attempt",
		Repositories: repos, Labels: []string{run.MacRunnerLabel}})
	if err != nil || job.ID != "mac-job" {
		t.Fatalf("mac reservation = %q, %v", job.ID, err)
	}
	// A client that reports no capabilities keeps the original shared queue.
	legacy := ExternalReservation{Repositories: repos}
	for _, tc := range []struct {
		profile string
		want    bool
	}{
		{run.MacRunnerLabel, true},
		{run.WSLCRunnerLabel, true},
		{"portable-build", true},
	} {
		job := run.Job{Plan: run.Plan{External: true, Repository: "me/repo", Profile: tc.profile}}
		if got := legacy.matches(job); got != tc.want {
			t.Fatalf("legacy reservation for %q = %v, want %v", tc.profile, got, tc.want)
		}
	}
	portable := run.Job{Plan: run.Plan{External: true, Repository: "me/repo", Profile: "portable-build"}}
	if !(&ExternalReservation{Repositories: repos, Labels: []string{run.WSLCRunnerLabel}}).matches(portable) {
		t.Fatal("portable labels keep the shared queue")
	}
}

func TestHasQueuedExternalFiltersByLabel(t *testing.T) {
	ctx := t.Context()
	store := openTest(t)
	item := run.Run{ID: "mac-run", DeliveryID: "mac-delivery", Repository: "me/repo", WorkflowPath: ".github/workflows/mac.yml"}
	if created, err := store.CreateRun(ctx, item, []run.Plan{{JobID: "mac-job", RunID: item.ID, Repository: item.Repository,
		Profile: run.MacRunnerLabel, SourceWorkflow: item.WorkflowPath, Ref: "refs/heads/main", External: true}}); err != nil || !created {
		t.Fatal(created, err)
	}
	if found, err := store.HasQueuedExternal(ctx, []string{run.MacRunnerLabel}); err != nil || !found {
		t.Fatalf("mac label: %v %v", found, err)
	}
	if found, err := store.HasQueuedExternal(ctx, []string{run.WSLCRunnerLabel}); err != nil || found {
		t.Fatalf("wslc label: %v %v", found, err)
	}
	if found, err := store.HasQueuedExternal(ctx, nil); err != nil || found {
		t.Fatalf("no labels: %v %v", found, err)
	}
	// Claiming the job removes it from the queue the scheduler looks at.
	if _, err := store.ReserveExternal(ctx, 1, ExternalReservation{RunnerID: "mac", AttemptID: "attempt",
		Repositories: map[string]bool{"me/repo": true}, Labels: []string{run.MacRunnerLabel}}); err != nil {
		t.Fatal(err)
	}
	if found, err := store.HasQueuedExternal(ctx, []string{run.MacRunnerLabel}); err != nil || found {
		t.Fatalf("reserved job still queued: %v %v", found, err)
	}
}

func TestBackupRestoreInvalidatesExternalLease(t *testing.T) {
	ctx := t.Context()
	store := openTest(t)
	req := ExternalReservation{RunnerID: "lab-mac", Repositories: map[string]bool{"me/repo": true}, AttemptID: "first-attempt"}
	item := run.Run{ID: "mac-run", DeliveryID: "mac-delivery", Repository: "me/repo", WorkflowPath: ".github/workflows/mac.yml"}
	plan := run.Plan{JobID: "mac-job", RunID: item.ID, Repository: item.Repository, Profile: run.MacRunnerLabel,
		SourceWorkflow: item.WorkflowPath, Ref: "refs/heads/main", External: true}
	if created, err := store.CreateRun(ctx, item, []run.Plan{plan}); err != nil || !created {
		t.Fatal(created, err)
	}
	if job, err := store.ReserveExternal(ctx, 1, req); err != nil || job.ID != plan.JobID {
		t.Fatal(job.ID, err)
	}
	if _, err := store.Claim(ctx, plan.JobID, req.AttemptID); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "backup.db")
	if err := store.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	if err := PrepareRestore(ctx, backup); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(ctx, backup)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	job, err := restored.GetJob(ctx, plan.JobID)
	if err != nil || job.State != run.Interrupted || !job.LeaseUntil.IsZero() {
		t.Fatalf("restored attempt: %+v, %v", job, err)
	}
	if _, err = restored.RenewExternal(ctx, plan.JobID, req.AttemptID); !errors.Is(err, ErrConflict) {
		t.Fatalf("restored runner lease could be renewed: %v", err)
	}
}
