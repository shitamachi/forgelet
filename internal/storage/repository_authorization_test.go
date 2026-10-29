package storage

import (
	"testing"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestAuthorizedReservationSkipsUnconfirmedWithoutTakingSlots(t *testing.T) {
	s := openTest(t)
	blocked := createTest(t, s, "blocked")
	allowed := createTest(t, s, "allowed")
	// Keep plans unchanged: authorization follows the owning run identity.
	if _, err := s.db.ExecContext(t.Context(), "UPDATE runs SET body=json_set(body,'$.repository','other/repo') WHERE id=?", allowed.RunID); err != nil {
		t.Fatal(err)
	}
	if jobs, err := s.ReserveAuthorized(t.Context(), 1, nil); err != nil || len(jobs) != 0 {
		t.Fatalf("empty authority scheduled work: %+v %v", jobs, err)
	}
	jobs, err := s.ReserveAuthorized(t.Context(), 1, []string{"other/repo"})
	if err != nil || len(jobs) != 1 || jobs[0].ID != allowed.JobID {
		t.Fatalf("blocked run starved authorized work: %+v %v", jobs, err)
	}
	paused, err := s.GetJob(t.Context(), blocked.JobID)
	if err != nil || paused.State != run.Queued {
		t.Fatalf("uncertain grant changed state: %+v %v", paused, err)
	}
	if n, err := s.CancelRepository(t.Context(), "other/repo", "authorization removed"); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	// A starting cancellation still owns its slot until its Pod is confirmed gone.
	if jobs, err := s.ReserveAuthorized(t.Context(), 1, []string{"me/repo"}); err != nil || len(jobs) != 0 {
		t.Fatalf("cancel intent prematurely released slot: %+v %v", jobs, err)
	}
	if err := s.Interrupt(t.Context(), allowed.JobID, "execution pod is absent"); err != nil {
		t.Fatal(err)
	}
	if jobs, err := s.ReserveAuthorized(t.Context(), 1, []string{"me/repo"}); err != nil || len(jobs) != 1 || jobs[0].ID != blocked.JobID {
		t.Fatal(jobs, err)
	}
}

func TestRepositoryRevocationCancelsQueuedAndActiveButPreservesHistory(t *testing.T) {
	s := openTest(t)
	active := createTest(t, s, "active")
	claimTest(t, s, active)
	queued := createTest(t, s, "queued")
	if n, err := s.CancelRepository(t.Context(), "ME/REPO", "GitHub installation suspended"); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	running, err := s.GetJob(t.Context(), active.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if running.State != run.Running || !running.CancelRequested || running.CancelReason != "GitHub installation suspended" || running.Result != nil {
		t.Fatalf("active result fabricated: %+v", running)
	}
	next, err := s.GetJob(t.Context(), queued.JobID)
	if err != nil || next.State != run.Cancelled || next.Result.Error != "GitHub installation suspended" {
		t.Fatal(next, err)
	}
	if n, err := s.CancelRepository(t.Context(), "me/repo", "repeat event"); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if item, err := s.GetRun(t.Context(), queued.RunID); err != nil || len(item.Jobs) != 1 {
		t.Fatal("revocation deleted history", item, err)
	}
}
