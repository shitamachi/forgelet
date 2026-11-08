package storage

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/shitamachi/forgelet/internal/run"
)

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
