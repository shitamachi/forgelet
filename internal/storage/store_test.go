package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.Context(), filepath.Join(t.TempDir(), "forgelet.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func createTest(t *testing.T, s *Store, id string) run.Plan {
	t.Helper()
	p := run.Plan{JobID: "job-" + id, RunID: "run-" + id, Name: "test", Profile: "small", Repository: "me/repo", SHA: "abc", Env: map[string]string{"CI": "true"}, Steps: []run.Step{{ID: "test", Run: "go test ./..."}}}
	r := run.Run{ID: p.RunID, DeliveryID: "delivery-" + id, Repository: p.Repository, SHA: p.SHA, WorkflowPath: ".github/workflows/test.yml", InstallationID: 42}
	if ok, err := s.CreateRun(t.Context(), r, []run.Plan{p}); err != nil || !ok {
		t.Fatalf("CreateRun: %v, %v", ok, err)
	}
	return p
}

func claimTest(t *testing.T, s *Store, p run.Plan) {
	t.Helper()
	if _, err := s.Reserve(t.Context(), 100); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(t.Context(), p.JobID, "pod-uid"); err != nil {
		t.Fatal(err)
	}
}

func TestEventsAndRunsSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forgelet.db")
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ev := run.Event{DeliveryID: "delivery-a", Repository: "me/repo", SHA: "abc", Payload: json.RawMessage(`{"ref":"refs/heads/main"}`)}
	for _, want := range []bool{true, false} {
		if got, err := s.PutEvent(t.Context(), ev); err != nil || got != want {
			t.Fatalf("PutEvent=%v,%v; want %v", got, err, want)
		}
	}
	p := createTest(t, s, "a")
	r := run.Run{ID: "different-id", DeliveryID: ev.DeliveryID, WorkflowPath: ".github/workflows/test.yml"}
	if ok, err := s.CreateRun(t.Context(), r, []run.Plan{p}); err != nil || ok {
		t.Fatalf("duplicate CreateRun=%v,%v", ok, err)
	}
	claimTest(t, s, p)
	if err := s.AppendLog(t.Context(), p.JobID, "pod-uid", run.Log{Seq: 1, Data: "persist me\n"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetJob(t.Context(), p.JobID)
	if err != nil || got.State != run.Running || got.PodUID != "pod-uid" || !reflect.DeepEqual(got.Plan, p) {
		t.Fatalf("restored job: %+v,%v", got, err)
	}
	events, err := s.PendingEvents(t.Context())
	if err != nil || len(events) != 1 || !reflect.DeepEqual(events[0], ev) {
		t.Fatalf("restored events: %+v,%v", events, err)
	}
	logs, err := s.Logs(t.Context(), p.JobID, 0)
	if err != nil || len(logs) != 1 || logs[0].Data != "persist me\n" {
		t.Fatalf("restored logs: %+v,%v", logs, err)
	}
	if err := s.FinishEvent(t.Context(), ev.DeliveryID, "unsupported workflow"); err != nil {
		t.Fatal(err)
	}
	events, err = s.PendingEvents(t.Context())
	if err != nil || len(events) != 0 {
		t.Fatalf("finished events are pending: %+v,%v", events, err)
	}
	events, err = s.Events(t.Context())
	if err != nil || len(events) != 1 || events[0].Error != "unsupported workflow" {
		t.Fatalf("event diagnostics: %+v,%v", events, err)
	}
	if err := s.FinishEvent(t.Context(), "missing", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing event=%v", err)
	}
}

func TestExclusiveDirectoryAndPrivateDatabase(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(t.Context(), filepath.Join(dir, "forgelet.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if other, err := Open(t.Context(), filepath.Join(dir, "other.db")); !errors.Is(err, ErrConflict) {
		if other != nil {
			_ = other.Close()
		}
		t.Fatalf("second writer: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dir, "forgelet.db"))
	if err != nil || fi.Mode().Perm() != 0600 {
		t.Fatalf("database mode: %+v,%v", fi, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	other, err := Open(t.Context(), filepath.Join(dir, "other.db"))
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	_ = other.Close()
}

func TestConcurrentReservationsNeverExceedQuota(t *testing.T) {
	s := openTest(t)
	for i := range 20 {
		createTest(t, s, fmt.Sprint(i))
	}
	var wg sync.WaitGroup
	ids := make(chan string, 20)
	for range 32 {
		wg.Go(func() {
			jobs, err := s.Reserve(t.Context(), 4)
			if err != nil {
				t.Error(err)
				return
			}
			for _, job := range jobs {
				if job.PodName != "f-"+job.ID || job.State != run.Starting {
					t.Errorf("invalid reservation: %+v", job)
				}
				ids <- job.ID
			}
		})
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Errorf("duplicate reservation: %s", id)
		}
		seen[id] = true
	}
	if len(seen) != 4 {
		t.Fatalf("reserved %d jobs; want 4", len(seen))
	}
	for id := range seen {
		if _, err := s.Claim(t.Context(), id, "uid-"+id); err != nil {
			t.Fatal(err)
		}
	}
	jobs, err := s.Reserve(t.Context(), 4)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("running jobs did not hold slots: %+v,%v", jobs, err)
	}
}

func TestClaimFencingAndCancellation(t *testing.T) {
	s := openTest(t)
	p := createTest(t, s, "one")
	if _, err := s.Claim(t.Context(), p.JobID, "uid"); !errors.Is(err, ErrConflict) {
		t.Fatalf("queued claim: %v", err)
	}
	_, _ = s.Reserve(t.Context(), 1)
	if err := s.RecordPod(t.Context(), p.JobID, "uid"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if got, err := s.Claim(t.Context(), p.JobID, "uid"); err != nil || !reflect.DeepEqual(got, p) {
			t.Fatalf("claim: %+v,%v", got, err)
		}
	}
	if _, err := s.Claim(t.Context(), p.JobID, "other"); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign claim: %v", err)
	}
	if err := s.RecordPod(t.Context(), p.JobID, "other"); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign pod recording: %v", err)
	}
	queued := createTest(t, s, "queued")
	if err := s.CancelRun(t.Context(), queued.RunID); err != nil {
		t.Fatal(err)
	}
	if job, _ := s.GetJob(t.Context(), queued.JobID); job.State != run.Cancelled {
		t.Fatalf("queued cancellation: %+v", job)
	}
	if err := s.CancelRun(t.Context(), p.RunID); err != nil {
		t.Fatal(err)
	}
	job, _ := s.GetJob(t.Context(), p.JobID)
	if job.State != run.Running || !job.CancelRequested {
		t.Fatalf("running cancellation prematurely released quota: %+v", job)
	}
	if err := s.Interrupt(t.Context(), p.JobID, "Pod stopped"); err != nil {
		t.Fatal(err)
	}
	job, _ = s.GetJob(t.Context(), p.JobID)
	if job.State != run.Cancelled || job.Result.State != run.Cancelled {
		t.Fatalf("cancelled pod terminal: %+v", job)
	}
	active, err := s.Active(t.Context())
	if err != nil || len(active) != 0 {
		t.Fatalf("cancelled jobs active: %+v,%v", active, err)
	}
}

func TestOrderedLogsAndTerminalMonotonicity(t *testing.T) {
	s := openTest(t)
	p := createTest(t, s, "logs")
	claimTest(t, s, p)
	first := run.Log{Seq: 1, StepID: "test", Data: "hello\n"}
	for _, tc := range []struct {
		name  string
		uid   string
		entry run.Log
		want  error
	}{
		{"foreign", "bad", first, ErrConflict},
		{"gap", "pod-uid", run.Log{Seq: 2}, ErrConflict},
		{"write", "pod-uid", first, nil},
		{"replay", "pod-uid", first, nil},
		{"changed replay", "pod-uid", run.Log{Seq: 1, Data: "different"}, ErrConflict},
		{"chunk bound", "pod-uid", run.Log{Seq: 2, Data: strings.Repeat("x", MaxLogChunk+1)}, ErrLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.AppendLog(t.Context(), p.JobID, tc.uid, tc.entry); !errors.Is(err, tc.want) {
				t.Fatalf("AppendLog=%v; want %v", err, tc.want)
			}
		})
	}
	for _, result := range []run.Result{{State: run.Running, LastLogSeq: 1}, {State: run.Interrupted, LastLogSeq: 1}, {State: run.Succeeded}, {State: run.Succeeded, LastLogSeq: 2}} {
		if err := s.Finish(t.Context(), p.JobID, "pod-uid", result); !errors.Is(err, ErrConflict) {
			t.Fatalf("invalid result %+v: %v", result, err)
		}
	}
	result := run.Result{State: run.Succeeded, LastLogSeq: 1, Steps: []run.StepResult{{ID: "test", State: run.Succeeded, Outputs: map[string]string{"answer": "42"}}}}
	if err := s.Finish(t.Context(), p.JobID, "foreign", result); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign finish: %v", err)
	}
	for range 2 {
		if err := s.Finish(t.Context(), p.JobID, "pod-uid", result); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Finish(t.Context(), p.JobID, "pod-uid", run.Result{State: run.Failed, LastLogSeq: 1}); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed terminal result: %v", err)
	}
	if err := s.AppendLog(t.Context(), p.JobID, "pod-uid", first); err != nil {
		t.Fatalf("retry after finish: %v", err)
	}
	if err := s.AppendLog(t.Context(), p.JobID, "pod-uid", run.Log{Seq: 2, Data: "late"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("late new log: %v", err)
	}
	if err := s.Interrupt(t.Context(), p.JobID, "late observation"); err != nil {
		t.Fatal(err)
	}
	r, err := s.GetRun(t.Context(), p.RunID)
	if err != nil || r.State != run.Succeeded {
		t.Fatalf("terminal state changed: %+v,%v", r, err)
	}
}

func TestLogsQuotaAndPagination(t *testing.T) {
	s := openTest(t)
	p := createTest(t, s, "quota")
	claimTest(t, s, p)
	for i := int64(1); i <= 257; i++ {
		if err := s.AppendLog(t.Context(), p.JobID, "pod-uid", run.Log{Seq: i, Data: "a"}); err != nil {
			t.Fatal(err)
		}
	}
	logs, err := s.Logs(t.Context(), p.JobID, 0)
	if err != nil || len(logs) != 256 {
		t.Fatalf("first page: %d,%v", len(logs), err)
	}
	logs, err = s.Logs(t.Context(), p.JobID, 256)
	if err != nil || len(logs) != 1 || logs[0].Seq != 257 {
		t.Fatalf("second page: %+v,%v", logs, err)
	}
	// Fill actual bounded records rather than changing implementation counters.
	for i := int64(258); ; i++ {
		err := s.AppendLog(t.Context(), p.JobID, "pod-uid", run.Log{Seq: i, Data: strings.Repeat("x", MaxLogChunk)})
		if errors.Is(err, ErrLimit) {
			if i > 580 {
				t.Fatalf("quota admitted too much data: %d", i)
			}
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if i > 600 {
			t.Fatal("quota not enforced")
		}
	}
}

func TestCheckOutboxStateRace(t *testing.T) {
	s := openTest(t)
	p := createTest(t, s, "checks")
	checks, err := s.PendingChecks(t.Context())
	if err != nil || len(checks) != 1 || checks[0].Job.State != run.Queued {
		t.Fatalf("initial checks: %+v,%v", checks, err)
	}
	claimTest(t, s, p)
	if err := s.SaveCheck(t.Context(), p.JobID, 123, run.Queued); err != nil {
		t.Fatal(err)
	}
	checks, err = s.PendingChecks(t.Context())
	if err != nil || len(checks) != 1 || checks[0].RemoteID != 123 || checks[0].Job.State != run.Running {
		t.Fatalf("stale acknowledgement lost newer state: %+v,%v", checks, err)
	}
	if err := s.SaveCheck(t.Context(), p.JobID, 123, run.Running); err != nil {
		t.Fatal(err)
	}
	checks, err = s.PendingChecks(t.Context())
	if err != nil || len(checks) != 0 {
		t.Fatalf("clean check: %+v,%v", checks, err)
	}
	if err := s.Finish(t.Context(), p.JobID, "pod-uid", run.Result{State: run.Succeeded}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCheck(t.Context(), p.JobID, 999, run.Succeeded); !errors.Is(err, ErrConflict) {
		t.Fatalf("remote identity overwritten: %v", err)
	}
	checks, err = s.PendingChecks(t.Context())
	if err != nil || len(checks) != 1 || checks[0].Job.State != run.Succeeded {
		t.Fatalf("terminal check missing: %+v,%v", checks, err)
	}
}

func TestBackupAndAtomicCreate(t *testing.T) {
	s := openTest(t)
	p := createTest(t, s, "backup")
	broken := run.Run{ID: "broken", DeliveryID: "broken", WorkflowPath: "test.yml"}
	if _, err := s.CreateRun(t.Context(), broken, []run.Plan{{JobID: "invalid", RunID: "wrong"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("invalid plan: %v", err)
	}
	if _, err := s.GetRun(t.Context(), broken.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("partial run committed: %v", err)
	}
	dest := filepath.Join(t.TempDir(), "backup.db")
	if err := s.Backup(t.Context(), dest); err != nil {
		t.Fatal(err)
	}
	if err := s.Backup(t.Context(), dest); !errors.Is(err, os.ErrExist) {
		t.Fatalf("backup overwrote destination: %v", err)
	}
	if restored, err := Open(t.Context(), dest); !errors.Is(err, ErrRestoreRequired) {
		if restored != nil {
			_ = restored.Close()
		}
		t.Fatalf("backup opened without preparation: %v", err)
	}
	if err := PrepareRestore(t.Context(), dest); err != nil {
		t.Fatal(err)
	}
	backup, err := Open(t.Context(), dest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = backup.Close() }()
	got, err := backup.GetJob(t.Context(), p.JobID)
	if err != nil || !reflect.DeepEqual(got.Plan, p) {
		t.Fatalf("backup plan: %+v,%v", got, err)
	}
	if got.State != run.Interrupted {
		t.Fatalf("restored work not quarantined: %+v", got)
	}
}

func TestRestoreQuarantinesPendingEventsAndLiveAttempts(t *testing.T) {
	s := openTest(t)
	p := createTest(t, s, "restore-running")
	claimTest(t, s, p)
	if err := s.AppendLog(t.Context(), p.JobID, "pod-uid", run.Log{Seq: 1, Data: "before snapshot"}); err != nil {
		t.Fatal(err)
	}
	_, err := s.PutEvent(t.Context(), run.Event{DeliveryID: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "restore.db")
	if err := s.Backup(t.Context(), dest); err != nil {
		t.Fatal(err)
	}
	// Snapshot marking and preparation must not affect the live database.
	live, err := s.GetJob(t.Context(), p.JobID)
	if err != nil || live.State != run.Running {
		t.Fatalf("live changed: %+v,%v", live, err)
	}
	if err := PrepareRestore(t.Context(), dest); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(t.Context(), dest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	job, err := restored.GetJob(t.Context(), p.JobID)
	if err != nil || job.State != run.Interrupted || job.Result.LastLogSeq != 1 {
		t.Fatalf("restore job: %+v,%v", job, err)
	}
	if pending, err := restored.PendingEvents(t.Context()); err != nil || len(pending) != 0 {
		t.Fatalf("restore pending=%+v,%v", pending, err)
	}
	if reserved, err := restored.Reserve(t.Context(), 4); err != nil || len(reserved) != 0 {
		t.Fatalf("restore reservations=%+v,%v", reserved, err)
	}
	if events, err := restored.Events(t.Context()); err != nil || len(events) != 1 || !strings.Contains(events[0].Error, "quarantined") {
		t.Fatalf("restore events=%+v,%v", events, err)
	}
}

func TestCrashRecovery(t *testing.T) {
	if path := os.Getenv("FORGELET_STORAGE_CRASH_DB"); path != "" {
		s, err := Open(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		p := createTest(t, s, "crash")
		claimTest(t, s, p)
		if err := s.AppendLog(t.Context(), p.JobID, "pod-uid", run.Log{Seq: 1, Data: "WAL survives exit"}); err != nil {
			t.Fatal(err)
		}
		// Bypass database Close and all test cleanup to leave the WAL uncheckpointed.
		os.Exit(0)
	}
	path := filepath.Join(t.TempDir(), "crash.db")
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCrashRecovery$")
	cmd.Env = append(os.Environ(), "FORGELET_STORAGE_CRASH_DB="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child failed: %v\n%s", err, out)
	}
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	job, err := s.GetJob(t.Context(), "job-crash")
	if err != nil || job.State != run.Running || job.PodUID != "pod-uid" {
		t.Fatalf("crash recovery: %+v,%v", job, err)
	}
	logs, err := s.Logs(t.Context(), job.ID, 0)
	if err != nil || len(logs) != 1 || logs[0].Data != "WAL survives exit" {
		t.Fatalf("crash logs: %+v,%v", logs, err)
	}
}

func TestRunOrderingAndAggregation(t *testing.T) {
	s := openTest(t)
	for i := range 102 {
		p := run.Plan{JobID: fmt.Sprintf("job-%d", i), RunID: fmt.Sprintf("run-%d", i)}
		r := run.Run{ID: p.RunID, DeliveryID: fmt.Sprint(i), WorkflowPath: "test.yml", CreatedAt: time.Unix(int64(i), 0)}
		if _, err := s.CreateRun(t.Context(), r, []run.Plan{p}); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := s.ListRuns(t.Context())
	if err != nil || len(runs) != 100 || runs[0].ID != "run-101" || runs[99].ID != "run-2" {
		t.Fatalf("run ordering/count: %d,%v", len(runs), err)
	}
	for _, tc := range []struct {
		states []run.State
		want   run.State
	}{
		{[]run.State{run.Queued}, run.Queued},
		{[]run.State{run.Queued, run.Running}, run.Running},
		{[]run.State{run.Succeeded, run.Failed}, run.Failed},
		{[]run.State{run.Succeeded, run.Cancelled}, run.Cancelled},
		{[]run.State{run.Succeeded, run.Succeeded}, run.Succeeded},
	} {
		var jobs []run.Job
		for _, state := range tc.states {
			jobs = append(jobs, run.Job{State: state})
		}
		if got := aggregate(jobs); got != tc.want {
			t.Errorf("aggregate(%v)=%s; want %s", tc.states, got, tc.want)
		}
	}
}
