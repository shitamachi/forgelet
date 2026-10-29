package storage

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestOutboxBackoffFairnessExhaustionAndRestart(t *testing.T) {
	s := openTest(t)
	now := time.Now().UTC()
	for i := range 101 {
		_, err := s.PutEvent(t.Context(), run.Event{DeliveryID: fmt.Sprint(i)})
		if err != nil {
			t.Fatal(err)
		}
	}
	events, _ := s.PendingEvents(t.Context(), now.Add(time.Hour))
	if len(events) != 100 {
		t.Fatal(len(events))
	}
	for _, e := range events {
		if err := s.RetryEvent(t.Context(), e.DeliveryID, "outage", now); err != nil {
			t.Fatal(err)
		}
	}
	next, _ := s.PendingEvents(t.Context(), now)
	if len(next) != 1 {
		t.Fatalf("new events starved: %d", len(next))
	}
	id := events[len(events)-1].DeliveryID
	for i := 1; i < RetryLimit; i++ {
		if err := s.RetryEvent(t.Context(), id, "outage", now); err != nil {
			t.Fatal(err)
		}
	}
	all, _ := s.Events(t.Context())
	found := false
	for _, e := range all {
		if e.DeliveryID == id {
			found = strings.Contains(e.Error, "retry limit reached")
		}
	}
	if !found {
		t.Fatal("missing final diagnostic")
	}
	if err := s.RetryFailedEvent(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if pending, _ := s.PendingEvents(t.Context(), now); len(pending) != 2 {
		t.Fatal(len(pending))
	}
	// Retry metadata is durable, rather than an in-memory scheduling hint.
	file := filepath.Join(t.TempDir(), "db")
	other, err := Open(t.Context(), file)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = other.PutEvent(t.Context(), run.Event{DeliveryID: "restart"})
	if err := other.RetryEvent(t.Context(), "restart", "offline", now); err != nil {
		t.Fatal(err)
	}
	_ = other.Close()
	other, err = Open(t.Context(), file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	if p, _ := other.PendingEvents(t.Context(), now); len(p) != 0 {
		t.Fatal("backoff lost across restart")
	}
	if p, _ := other.PendingEvents(t.Context(), now.Add(3*time.Second)); len(p) != 1 {
		t.Fatal("retry did not become due")
	}
}
func TestChecksBackoffTerminalDiagnosticsAndCASReplacement(t *testing.T) {
	s := openTest(t)
	now := time.Now().UTC()
	_, plan := oldFinishedRun(t, s, "checks-retry", 42, now)
	job, err := s.GetJob(t.Context(), plan.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCheck(t.Context(), job.ID, 11, job.State); err != nil {
		t.Fatal(err)
	}
	if err := s.RetryRunChecks(t.Context(), job.RunID); err != nil {
		t.Fatal(err)
	}
	if err := s.RetryCheck(t.Context(), job.ID, job.State, "offline", now); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.PendingChecks(t.Context(), now); len(c) != 0 {
		t.Fatal("backoff ignored")
	}
	if c, _ := s.PendingChecks(t.Context(), now.Add(3*time.Second)); len(c) != 1 {
		t.Fatal("missing retry")
	}
	for i := 1; i < RetryLimit; i++ {
		if err := s.RetryCheck(t.Context(), job.ID, job.State, "offline", now); err != nil {
			t.Fatal(err)
		}
	}
	if c, _ := s.PendingChecks(t.Context(), now.Add(time.Hour)); len(c) != 0 {
		t.Fatal("exhausted check keeps spinning")
	}
	j, _ := s.GetJob(t.Context(), job.ID)
	if !strings.Contains(j.CheckError, "retry limit") {
		t.Fatal(j.CheckError)
	}
	if err := s.RetryRunChecks(t.Context(), job.RunID); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceCheck(t.Context(), job.ID, 99, 12, job.State); err == nil {
		t.Fatal("stale CAS accepted")
	}
	if err := s.ReplaceCheck(t.Context(), job.ID, 11, 12, job.State); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.PendingChecks(t.Context(), now); len(c) != 0 {
		t.Fatal("replacement left dirty")
	}
}
func TestRuntimeRetryBudgetReleasesCapacity(t *testing.T) {
	s := openTest(t)
	p := createGraph(t, s, "retry-runtime", run.Plan{Key: "root"})
	jobs, err := s.Reserve(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i := range RetryLimit {
		if err := s.RetryRuntime(t.Context(), p["root"].JobID, "offline", false, now.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	j, _ := s.GetJob(t.Context(), jobs[0].ID)
	if j.State != run.Interrupted || j.RuntimeFailures != RetryLimit {
		t.Fatal(j)
	}
	if active, _ := s.Active(t.Context()); len(active) != 0 {
		t.Fatal("budget still occupied")
	}
}
func TestRunListOmitsLargeExecutionDataWithoutChangingClaim(t *testing.T) {
	s := openTest(t)
	p := createGraph(t, s, "summary", run.Plan{Key: "root", Event: []byte(`{"private":"payload"}`), Env: map[string]string{"large": "value"}, Steps: []run.Step{{Run: "echo original"}}})
	list, err := s.ListRunSummaries(t.Context())
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	if len(list[0].Jobs[0].Plan.Event) != 0 || len(list[0].Jobs[0].Plan.Steps) != 0 {
		t.Fatal("summary contains execution payload")
	}
	_, _ = s.Reserve(t.Context(), 1)
	if err := s.RecordPod(t.Context(), p["root"].JobID, "uid"); err != nil {
		t.Fatal(err)
	}
	plan, err := s.Claim(t.Context(), p["root"].JobID, "uid")
	if err != nil || len(plan.Event) == 0 || len(plan.Steps) != 1 {
		t.Fatal(plan, err)
	}
}

func TestChecksBackoffDoesNotStarveLaterJobs(t *testing.T) {
	s := openTest(t)
	now := time.Now().UTC()
	for i := range 101 {
		oldFinishedRun(t, s, fmt.Sprintf("fair-%03d", i), 42, now)
	}
	pending, err := s.PendingChecks(t.Context(), now)
	if err != nil || len(pending) != 100 {
		t.Fatal(len(pending), err)
	}
	for _, c := range pending {
		if err := s.RetryCheck(t.Context(), c.Job.ID, c.Job.State, "permanent outage", now); err != nil {
			t.Fatal(err)
		}
	}
	later, err := s.PendingChecks(t.Context(), now)
	if err != nil || len(later) != 1 {
		t.Fatal("later check starved", len(later), err)
	}
}
func TestV5RetryMigrationPreservesPendingWork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	p := createGraph(t, s, "migration", run.Plan{Key: "root"})
	_, err = s.PutEvent(t.Context(), run.Event{DeliveryID: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	// Reconstruct the predecessor layout with existing queued work.
	_, err = s.db.ExecContext(t.Context(), `DROP TABLE progress_events;DROP INDEX events_retry;DROP INDEX checks_retry;
 ALTER TABLE events DROP COLUMN attempts;ALTER TABLE events DROP COLUMN next_attempt_at;
 ALTER TABLE jobs DROP COLUMN check_attempts;ALTER TABLE jobs DROP COLUMN check_next_attempt_at;
 PRAGMA user_version=5;`)
	if err != nil {
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
	events, err := s.PendingEvents(t.Context())
	if err != nil || len(events) != 1 {
		t.Fatal(events, err)
	}
	if err := s.RetryEvent(t.Context(), "pending", "offline", time.Now()); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.Reserve(t.Context(), 1)
	if err != nil || len(jobs) != 1 || jobs[0].ID != p["root"].JobID {
		t.Fatal(jobs, err)
	}
}

func TestResolvedNeedsCannotExceedClaimLimitOrBlockLaterRuns(t *testing.T) {
	for _, size := range []int{100, 850 << 10} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			s := openTest(t)
			p := createGraph(t, s, "needs-size",
				run.Plan{Key: "left"}, run.Plan{Key: "right"},
				run.Plan{Key: "join", Needs: []string{"left", "right"}, ContinueOnError: true, Event: []byte(`{"padding":"` + strings.Repeat("x", size) + `"}`)},
				run.Plan{Key: "after", Needs: []string{"join"}},
			)
			jobs, err := s.Reserve(t.Context(), 2)
			if err != nil || len(jobs) != 2 {
				t.Fatal(jobs, err)
			}
			for _, key := range []string{"left", "right"} {
				job := p[key]
				if err := s.RecordPod(t.Context(), job.JobID, "uid-"+job.JobID); err != nil {
					t.Fatal(err)
				}
				completeJob(t, s, job.JobID, run.Succeeded, map[string]string{"large": strings.Repeat("y", 60<<10)})
			}
			later := createGraph(t, s, "following", run.Plan{Key: "independent"})
			jobs, err = s.Reserve(t.Context(), 1)
			if err != nil || len(jobs) != 1 {
				t.Fatal(jobs, err)
			}
			join, err := s.GetJob(t.Context(), p["join"].JobID)
			if err != nil {
				t.Fatal(err)
			}
			if size == 100 {
				if jobs[0].ID != join.ID || join.State != run.Starting || len(join.Plan.NeedsContext) != 2 {
					t.Fatal(join)
				}
			} else {
				if join.State != run.Failed || join.Result == nil || !strings.Contains(join.Result.Error, "resolved execution plan") || join.Plan.NeedsContext != nil {
					t.Fatal(join)
				}
				after, err := s.GetJob(t.Context(), p["after"].JobID)
				if err != nil || after.State != run.Skipped {
					t.Fatal(after, err)
				}
				if jobs[0].ID != later["independent"].JobID {
					t.Fatal("oversized snapshot blocked later run")
				}
			}
		})
	}
}
