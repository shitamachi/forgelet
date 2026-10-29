package storage

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestProgressIdentityReplayRestartAndFinalResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "progress.db")
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	p := createTest(t, s, "progress")
	claimTest(t, s, p)
	at := time.Now().UTC()
	events := []run.ProgressEvent{
		{Seq: 1, Kind: "phase", Phase: "executing", At: at},
		{Seq: 2, Kind: "step", StepID: "test", State: run.Running, At: at.Add(time.Second)},
		{Seq: 3, Kind: "annotation", StepID: "test", Level: "warning", Message: "coverage was not uploaded", At: at.Add(2 * time.Second)},
		{Seq: 4, Kind: "step", StepID: "test", State: run.Succeeded, At: at.Add(3 * time.Second)},
	}
	if err := s.AppendProgress(t.Context(), p.JobID, "wrong-pod", events[0]); !errors.Is(err, ErrConflict) {
		t.Fatalf("identity: %v", err)
	}
	if err := s.AppendProgress(t.Context(), p.JobID, "pod-uid", events[1]); !errors.Is(err, ErrConflict) {
		t.Fatalf("gap: %v", err)
	}
	for i, e := range events {
		if err := s.AppendProgress(t.Context(), p.JobID, "pod-uid", e); err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			j, err := s.GetJob(t.Context(), p.JobID)
			if err != nil || j.Progress == nil || j.Progress.Steps[0].State != run.Running {
				t.Fatalf("restart: %+v %v", j, err)
			}
		}
	}
	j, err := s.GetJob(t.Context(), p.JobID)
	if err != nil || j.Result != nil || j.State != run.Running {
		t.Fatalf("progress became result: %+v %v", j, err)
	}
	// The final command outcome remains authoritative even if provisional progress
	// observed successful shell completion before file commands failed.
	if err := s.Finish(t.Context(), p.JobID, "pod-uid", run.Result{State: run.Failed, Error: "file commands failed"}); err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if err := s.AppendProgress(t.Context(), p.JobID, "pod-uid", e); err != nil {
			t.Fatalf("ACK replay after terminal: %v", err)
		}
	}
	changed := events[2]
	changed.Message = "different"
	if err := s.AppendProgress(t.Context(), p.JobID, "pod-uid", changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed replay: %v", err)
	}
	next := run.ProgressEvent{Seq: 5, Kind: "phase", Phase: "cleanup", At: at.Add(4 * time.Second)}
	if err := s.AppendProgress(t.Context(), p.JobID, "pod-uid", next); !errors.Is(err, ErrConflict) {
		t.Fatalf("new progress after terminal: %v", err)
	}
	views, err := s.ListRunSummaries(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(views[0].Jobs[0].Progress.Steps) != 0 || len(views[0].Jobs[0].Progress.Annotations) != 0 {
		t.Fatal("summary retained detailed progress")
	}
	if _, err := s.db.ExecContext(t.Context(), "DELETE FROM jobs WHERE id=?", p.JobID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM progress_events WHERE job_id=?", p.JobID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("journal not cascaded: %d %v", count, err)
	}
}

func TestProgressMigratesVersionSixWithoutChangingActiveJob(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v6.db")
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	p := createTest(t, s, "v6")
	claimTest(t, s, p)
	if _, err := s.db.ExecContext(t.Context(), "DROP TABLE progress_events; PRAGMA user_version=6"); err != nil {
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
	j, err := s.GetJob(t.Context(), p.JobID)
	if err != nil || j.State != run.Running || j.PodUID != "pod-uid" || j.Progress != nil {
		t.Fatalf("migration changed old attempt: %+v %v", j, err)
	}
	if err := s.AppendProgress(t.Context(), p.JobID, "pod-uid", run.ProgressEvent{Seq: 1, Kind: "phase", Phase: "executing", At: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
}
