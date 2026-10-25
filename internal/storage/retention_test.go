package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run"
)

var testRetention = RetentionPolicy{History: 30 * 24 * time.Hour, Logs: 7 * 24 * time.Hour, Events: 30 * 24 * time.Hour, BatchSize: 100}

func ageFinishedJob(t *testing.T, s *Store, jobID string, at time.Time) {
	t.Helper()
	job, err := s.GetJob(t.Context(), jobID)
	if err != nil {
		t.Fatal(err)
	}
	job.UpdatedAt = at
	body, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), "UPDATE jobs SET body=?,terminal_at=? WHERE id=?", body, at.UnixNano(), jobID); err != nil {
		t.Fatal(err)
	}
}

func oldFinishedRun(t *testing.T, s *Store, id string, installationID int64, at time.Time) (run.Run, run.Plan) {
	t.Helper()
	record := run.Run{ID: "run-" + id, DeliveryID: "delivery-" + id, WorkflowPath: "ci.yml", InstallationID: installationID, CreatedAt: at.Add(-time.Hour)}
	plan := run.Plan{JobID: "job-" + id, RunID: record.ID, Profile: "small", Name: id}
	if ok, err := s.CreateRun(t.Context(), record, []run.Plan{plan}); err != nil || !ok {
		t.Fatalf("create historical run: %v, %v", ok, err)
	}
	if _, err := s.Reserve(t.Context(), 1024); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(t.Context(), plan.JobID, "uid"); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendLog(t.Context(), plan.JobID, "uid", run.Log{Seq: 1, Data: "historical log"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(t.Context(), plan.JobID, "uid", run.Result{State: run.Succeeded, LastLogSeq: 1}); err != nil {
		t.Fatal(err)
	}
	ageFinishedJob(t, s, plan.JobID, at)
	return record, plan
}

func oldEvent(t *testing.T, s *Store, delivery string, done bool, at time.Time) run.Event {
	t.Helper()
	event := run.Event{DeliveryID: delivery, Repository: "me/repo", SHA: "abc", Payload: json.RawMessage(`{"ref":"refs/heads/main","commits":["payload"]}`)}
	if ok, err := s.PutEvent(t.Context(), event); err != nil || !ok {
		t.Fatalf("old event: %v, %v", ok, err)
	}
	if done {
		if err := s.FinishEvent(t.Context(), delivery, "diagnostic retained"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(t.Context(), "UPDATE events SET created_at=? WHERE delivery_id=?", at.UnixNano(), delivery); err != nil {
		t.Fatal(err)
	}
	return event
}

func TestRetentionExpiresLogsThenHistoryWithoutReplaying(t *testing.T) {
	s := openTest(t)
	now := time.Now().UTC()
	record, plan := oldFinishedRun(t, s, "expire", 0, now.Add(-8*24*time.Hour))
	event := oldEvent(t, s, record.DeliveryID, true, now.Add(-40*24*time.Hour))
	result, err := s.Prune(t.Context(), now, testRetention, map[string]string{})
	if err != nil || result.Runs != 0 || result.Jobs != 0 || result.Logs != 1 || result.Events != 1 {
		t.Fatalf("first prune: %+v, %v", result, err)
	}
	job, err := s.GetJob(t.Context(), plan.JobID)
	if err != nil || !job.LogsExpired || job.Result.LastLogSeq != 1 || !job.UpdatedAt.Equal(now.Add(-8*24*time.Hour)) {
		t.Fatalf("log expiry corrupted result: %+v, %v", job, err)
	}
	if _, err := s.Logs(t.Context(), plan.JobID, 0); !errors.Is(err, ErrLogsExpired) {
		t.Fatalf("expired logs lack distinct error: %v", err)
	}
	if err := s.Finish(t.Context(), plan.JobID, "uid", *job.Result); err != nil {
		t.Fatalf("result replay after log expiration: %v", err)
	}
	events, err := s.Events(t.Context())
	if err != nil || len(events) != 1 || len(events[0].Payload) != 0 || events[0].Error != "diagnostic retained" || events[0].SHA != event.SHA {
		t.Fatalf("event metadata: %+v, %v", events, err)
	}
	result, err = s.Prune(t.Context(), now.Add(31*24*time.Hour), testRetention, nil)
	if err != nil || result.Runs != 1 || result.Jobs != 1 || result.Logs != 0 || result.Events != 0 {
		t.Fatalf("history prune: %+v, %v", result, err)
	}
	if _, err := s.GetRun(t.Context(), record.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted run visible: %v", err)
	}
	if _, err := s.GetJob(t.Context(), plan.JobID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted job visible: %v", err)
	}
	if _, err := s.Logs(t.Context(), plan.JobID, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("history disappearance did not return not found: %v", err)
	}
	if ok, err := s.PutEvent(t.Context(), event); err != nil || ok {
		t.Fatalf("expired event redelivered: %v, %v", ok, err)
	}
	if ok, err := s.CreateRun(t.Context(), record, []run.Plan{plan}); err != nil || ok {
		t.Fatalf("expired run recreated: %v, %v", ok, err)
	}
	// The delivery/workflow tombstone also fences a caller using a fresh run ID.
	record.ID, plan.RunID, plan.JobID = "new-id", "new-id", "new-job"
	if ok, err := s.CreateRun(t.Context(), record, []run.Plan{plan}); err != nil || ok {
		t.Fatalf("expired run identity bypassed: %v, %v", ok, err)
	}
	if jobs, err := s.Reserve(t.Context(), 4); err != nil || len(jobs) != 0 {
		t.Fatalf("pruned run executed again: %+v, %v", jobs, err)
	}
}

func TestRetentionProtectsActiveRunsPodsAndPendingChecks(t *testing.T) {
	s := openTest(t)
	now := time.Now().UTC()
	old := now.Add(-40 * 24 * time.Hour)
	manual, _ := oldFinishedRun(t, s, "manual", 0, old)
	checkRun, check := oldFinishedRun(t, s, "check", 42, old)
	podRun, pod := oldFinishedRun(t, s, "pod", 0, old)
	plans := createGraph(t, s, "unfinished", run.Plan{Key: "root"}, run.Plan{Key: "child", Needs: []string{"root"}})
	_, _ = s.Reserve(t.Context(), 4)
	completeJob(t, s, plans["root"].JobID, run.Succeeded, nil)
	ageFinishedJob(t, s, plans["root"].JobID, old)
	active := createTest(t, s, "active")
	claimTest(t, s, active)
	queued := createTest(t, s, "queued")
	oldEvent(t, s, podRun.DeliveryID, true, old)
	oldEvent(t, s, "delivery-unfinished", true, old)
	oldEvent(t, s, "pending", false, old)
	oldEvent(t, s, "done", true, old)
	result, err := s.Prune(t.Context(), now, testRetention, map[string]string{pod.JobID: "different-uid"})
	if err != nil || result.Runs != 1 || result.Jobs != 1 || result.Logs != 2 || result.Events != 1 {
		t.Fatalf("protected prune: %+v, %v", result, err)
	}
	if _, err := s.GetRun(t.Context(), manual.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("manual check_dirty blocked history: %v", err)
	}
	for _, id := range []string{checkRun.ID, podRun.ID, "unfinished", active.RunID, queued.RunID} {
		if _, err := s.GetRun(t.Context(), id); err != nil {
			t.Errorf("protected run %s deleted: %v", id, err)
		}
	}
	if _, err := s.Logs(t.Context(), check.JobID, 0); !errors.Is(err, ErrLogsExpired) {
		t.Fatalf("pending check must not indefinitely retain logs: %v", err)
	}
	for _, id := range []string{pod.JobID, plans["root"].JobID, active.JobID, queued.JobID} {
		job, err := s.GetJob(t.Context(), id)
		if err != nil || job.LogsExpired {
			t.Errorf("protected logs expired: %+v, %v", job, err)
		}
	}
	events, err := s.Events(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.DeliveryID != "done" && len(event.Payload) == 0 {
			t.Errorf("protected event lost payload: %+v", event)
		}
	}
	if err := s.SaveCheck(t.Context(), check.JobID, 123, run.Succeeded); err != nil {
		t.Fatal(err)
	}
	result, err = s.Prune(t.Context(), now, testRetention, map[string]string{})
	if err != nil || result.Runs != 2 || result.Jobs != 2 || result.Logs != 1 || result.Events != 1 {
		t.Fatalf("released protections not cleared: %+v, %v", result, err)
	}
}

func TestRetentionBatchesAndValidation(t *testing.T) {
	s := openTest(t)
	now := time.Now().UTC()
	for i := range 3 {
		record, _ := oldFinishedRun(t, s, fmt.Sprint(i), 0, now.Add(-40*24*time.Hour))
		oldEvent(t, s, record.DeliveryID, true, now.Add(-40*24*time.Hour))
	}
	policy := testRetention
	policy.BatchSize = 1
	for range 3 {
		result, err := s.Prune(t.Context(), now, policy, nil)
		if err != nil || result != (PruneResult{Runs: 1, Jobs: 1, Logs: 1, Events: 1}) {
			t.Fatalf("bounded batch: %+v, %v", result, err)
		}
	}
	if result, err := s.Prune(t.Context(), now, policy, nil); err != nil || result != (PruneResult{}) {
		t.Fatalf("non-idempotent prune: %+v, %v", result, err)
	}
	for _, invalid := range []RetentionPolicy{
		{}, {History: -time.Hour, Logs: time.Hour, Events: time.Hour, BatchSize: 1},
		{History: time.Hour, Logs: 0, Events: time.Hour, BatchSize: 1},
		{History: time.Hour, Logs: time.Hour, Events: 0, BatchSize: 1},
		{History: time.Hour, Logs: time.Hour, Events: time.Hour, BatchSize: 1001},
	} {
		if _, err := s.Prune(t.Context(), now, invalid, nil); !errors.Is(err, ErrLimit) {
			t.Fatalf("invalid policy accepted: %+v, %v", invalid, err)
		}
	}
	if _, err := s.Prune(t.Context(), time.Time{}, testRetention, nil); !errors.Is(err, ErrLimit) {
		t.Fatalf("zero prune time: %v", err)
	}
}

func TestPendingChecksNotStarvedByManualRuns(t *testing.T) {
	s := openTest(t)
	for i := range 105 {
		createGraph(t, s, fmt.Sprintf("manual-%03d", i), run.Plan{Key: "test"})
	}
	p := createTest(t, s, "github")
	checks, err := s.PendingChecks(t.Context())
	if err != nil || len(checks) != 1 || checks[0].Job.ID != p.JobID {
		t.Fatalf("manual runs starved GitHub checks: %+v, %v", checks, err)
	}
	// Paging the queue must also reach runs beyond its first 64 identities.
	jobs, err := s.Reserve(t.Context(), 200)
	if err != nil || len(jobs) != 106 {
		t.Fatalf("queued run paging: %d, %v", len(jobs), err)
	}
}

func writeV1Database(t *testing.T, path string, marked bool) (run.Run, run.Plan) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(t.Context(), schema); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-40 * 24 * time.Hour)
	record := run.Run{ID: "legacy", DeliveryID: "legacy-delivery", WorkflowPath: "ci.yml", InstallationID: 42, CreatedAt: old}
	plan := run.Plan{JobID: "legacy-job", RunID: record.ID, Profile: "small"}
	job := run.Job{ID: plan.JobID, RunID: record.ID, Plan: plan, State: run.Running, PodUID: "old-pod", CreatedAt: old, UpdatedAt: old}
	recordBody, _ := json.Marshal(record)
	jobBody, _ := json.Marshal(job)
	eventBody, _ := json.Marshal(run.Event{DeliveryID: record.DeliveryID, Payload: json.RawMessage(`{"ref":"main"}`)})
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{"INSERT INTO runs(id,delivery_id,workflow_path,body,created_at) VALUES(?,?,?,?,?)", []any{record.ID, record.DeliveryID, record.WorkflowPath, recordBody, old.UnixNano()}},
		{"INSERT INTO jobs(id,run_id,state,body,created_at) VALUES(?,?,?,?,?)", []any{job.ID, job.RunID, job.State, jobBody, old.UnixNano()}},
		{"INSERT INTO logs(job_id,seq,step_id,data) VALUES(?,1,'step','persisted')", []any{job.ID}},
		{"INSERT INTO events(delivery_id,body,created_at) VALUES(?,?,?)", []any{record.DeliveryID, eventBody, old.UnixNano()}},
	} {
		if _, err := db.ExecContext(t.Context(), statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if marked {
		if _, err := db.ExecContext(t.Context(), "INSERT INTO metadata(key,value) VALUES('restore_required','1')"); err != nil {
			t.Fatal(err)
		}
	}
	return record, plan
}

func TestV1MigrationPreservesActivePlansAndDedupe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	record, plan := writeV1Database(t, path, false)
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	var version, vacuum int
	if err := s.db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil || version != 6 {
		t.Fatalf("schema version=%d, %v", version, err)
	}
	if err := s.db.QueryRowContext(t.Context(), "PRAGMA auto_vacuum").Scan(&vacuum); err != nil || vacuum != 0 {
		t.Fatalf("legacy database was fully rebuilt: %d, %v", vacuum, err)
	}
	claimed, err := s.Claim(t.Context(), plan.JobID, "old-pod")
	if err != nil || claimed.JobID != plan.JobID || claimed.Key != "" {
		t.Fatalf("legacy plan changed: %+v, %v", claimed, err)
	}
	logs, err := s.Logs(t.Context(), plan.JobID, 0)
	if err != nil || len(logs) != 1 || logs[0].Data != "persisted" {
		t.Fatalf("legacy logs lost: %+v, %v", logs, err)
	}
	checks, err := s.PendingChecks(t.Context())
	if err != nil || len(checks) != 1 || checks[0].Run.InstallationID != 42 {
		t.Fatalf("installation migration lost checks: %+v, %v", checks, err)
	}
	if err := s.Finish(t.Context(), plan.JobID, "old-pod", run.Result{State: run.Succeeded, LastLogSeq: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCheck(t.Context(), plan.JobID, 1, run.Succeeded); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishEvent(t.Context(), record.DeliveryID, ""); err != nil {
		t.Fatal(err)
	}
	ageFinishedJob(t, s, plan.JobID, time.Now().Add(-40*24*time.Hour))
	if result, err := s.Prune(t.Context(), time.Now(), testRetention, nil); err != nil || result.Runs != 1 {
		t.Fatalf("legacy history not pruned: %+v, %v", result, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.CreateRun(t.Context(), record, []run.Plan{plan}); err != nil || ok {
		t.Fatalf("tombstone lost on restart: %v, %v", ok, err)
	}
	if ok, err := s.PutEvent(t.Context(), run.Event{DeliveryID: record.DeliveryID}); err != nil || ok {
		t.Fatalf("event dedupe lost on restart: %v, %v", ok, err)
	}
}

func TestV1MarkedRestoreStillQuarantinesAndNewDatabasesReclaim(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup.db")
	_, plan := writeV1Database(t, path, true)
	if s, err := Open(t.Context(), path); !errors.Is(err, ErrRestoreRequired) {
		if s != nil {
			_ = s.Close()
		}
		t.Fatalf("legacy backup started without preparation: %v", err)
	}
	if err := PrepareRestore(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	job, err := s.GetJob(t.Context(), plan.JobID)
	if err != nil || job.State != run.Interrupted || job.Result.LastLogSeq != 1 {
		t.Fatalf("legacy restore replay risk: %+v, %v", job, err)
	}
	if pending, err := s.PendingEvents(t.Context()); err != nil || len(pending) != 0 {
		t.Fatalf("legacy events replay risk: %+v, %v", pending, err)
	}
	newDB := openTest(t)
	var vacuum int
	if err := newDB.db.QueryRowContext(t.Context(), "PRAGMA auto_vacuum").Scan(&vacuum); err != nil || vacuum != 2 {
		t.Fatalf("new database lacks incremental reclaim: %d, %v", vacuum, err)
	}
}

func TestReadsRemainConsistentDuringPrune(t *testing.T) {
	s := openTest(t)
	now := time.Now().UTC()
	type pair struct{ runID, jobID string }
	var records []pair
	for i := range 20 {
		record, plan := oldFinishedRun(t, s, fmt.Sprintf("concurrent-%02d", i), 0, now.Add(-40*24*time.Hour))
		records = append(records, pair{record.ID, plan.JobID})
	}
	start := make(chan struct{})
	var ready, done sync.WaitGroup
	for reader := range 8 {
		ready.Add(1)
		done.Go(func() {
			ready.Done()
			<-start
			for i := range 50 {
				ids := records[(reader+i)%len(records)]
				record, err := s.GetRun(t.Context(), ids.runID)
				if err != nil && !errors.Is(err, ErrNotFound) {
					t.Errorf("concurrent run read: %v", err)
					return
				}
				if err == nil && (record.State != run.Succeeded || len(record.Jobs) != 1) {
					t.Errorf("run and jobs came from different snapshots: %+v", record)
					return
				}
				logs, err := s.Logs(t.Context(), ids.jobID, 0)
				if err != nil && !errors.Is(err, ErrLogsExpired) && !errors.Is(err, ErrNotFound) {
					t.Errorf("concurrent log read: %v", err)
					return
				}
				if err == nil && (len(logs) != 1 || logs[0].Data != "historical log") {
					t.Errorf("log cleanup incorrectly returned successful empty data: %+v", logs)
					return
				}
				list, err := s.ListRuns(t.Context())
				if err != nil {
					t.Errorf("run disappeared inside list read: %v", err)
					return
				}
				for _, record := range list {
					if len(record.Jobs) != 1 || record.State != run.Succeeded {
						t.Errorf("partial listed run: %+v", record)
						return
					}
				}
			}
		})
	}
	ready.Wait()
	close(start)
	policy := testRetention
	policy.BatchSize = 1
	for range records {
		if _, err := s.Prune(t.Context(), now, policy, nil); err != nil {
			t.Fatal(err)
		}
	}
	done.Wait()
}
