package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/shitamachi/forgelet/internal/run"
)

func (s *Store) CreateRun(ctx context.Context, record run.Run, plans []run.Plan) (bool, error) {
	if record.ID == "" || record.DeliveryID == "" || record.WorkflowPath == "" || len(plans) == 0 {
		return false, fmt.Errorf("run requires identity, workflow and plans: %w", ErrConflict)
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	record.State, record.Jobs = run.Queued, nil
	record.Concurrency, record.CancelInProgress = plans[0].WorkflowConcurrency, plans[0].WorkflowCancelInProgress
	body, err := json.Marshal(record)
	if err != nil {
		return false, fmt.Errorf("encode run: %w", err)
	}
	created := false
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		var expired int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM run_tombstones WHERE (delivery_id=? AND workflow_path=?) OR id=?", record.DeliveryID, record.WorkflowPath, record.ID).Scan(&expired); err != nil {
			return fmt.Errorf("check expired run identity: %w", err)
		}
		if expired != 0 {
			return nil
		}
		r, err := tx.ExecContext(ctx, "INSERT INTO runs(id,delivery_id,workflow_path,body,created_at,installation_id) VALUES(?,?,?,?,?,?) ON CONFLICT(delivery_id,workflow_path) DO NOTHING", record.ID, record.DeliveryID, record.WorkflowPath, body, record.CreatedAt.UnixNano(), record.InstallationID)
		if err != nil {
			return fmt.Errorf("persist run: %w", databaseError(err))
		}
		n, err := r.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		if err := validateGraph(record.ID, plans); err != nil {
			return err
		}
		for _, plan := range plans {
			if plan.JobID == "" || plan.RunID != record.ID {
				return fmt.Errorf("plan identity mismatch: %w", ErrConflict)
			}
			job := run.Job{ID: plan.JobID, RunID: record.ID, Name: plan.Name, Profile: plan.Profile, PodName: "f-" + plan.JobID, State: run.Queued, Plan: plan, CreatedAt: record.CreatedAt, UpdatedAt: record.CreatedAt}
			b, err := json.Marshal(job)
			if err != nil {
				return fmt.Errorf("encode job: %w", err)
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO jobs(id,run_id,state,body,created_at) VALUES(?,?,?,?,?)", job.ID, job.RunID, job.State, b, job.CreatedAt.UnixNano()); err != nil {
				return fmt.Errorf("persist job: %w", databaseError(err))
			}
		}
		if err := applyConcurrencyReplacement(ctx, tx, record); err != nil {
			return err
		}
		created = true
		return nil
	})
	return created && err == nil, err
}

func (s *Store) GetRun(ctx context.Context, id string) (run.Run, error) {
	var record run.Run
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var err error
		record, err = getRun(ctx, tx, id)
		return err
	})
	return record, err
}

func getRun(ctx context.Context, q queryer, id string, summary ...bool) (run.Run, error) {
	var record run.Run
	var body []byte
	if err := q.QueryRowContext(ctx, "SELECT body FROM runs WHERE id=?", id).Scan(&body); err != nil {
		return record, fmt.Errorf("get run %s: %w", id, notFound(err))
	}
	if err := json.Unmarshal(body, &record); err != nil {
		return record, fmt.Errorf("decode run: %w", err)
	}
	query := "SELECT body FROM jobs WHERE run_id=? ORDER BY created_at,id"
	if len(summary) > 0 && summary[0] {
		query = "SELECT json_remove(body,'$.plan.event','$.plan.steps','$.plan.env','$.plan.vars','$.plan.inputs','$.plan.call_inputs','$.plan.call_values','$.plan.needs_context','$.result.steps','$.result.outputs','$.progress.steps','$.progress.annotations') FROM jobs WHERE run_id=? ORDER BY created_at,id"
	}
	jobs, err := queryJobs(ctx, q, query, id)
	if err != nil {
		return record, err
	}
	record.Jobs, record.State = jobs, aggregate(jobs)
	return record, nil
}

func aggregate(jobs []run.Job) run.State {
	allQueued, allTerminal := true, true
	for _, j := range jobs {
		allQueued = allQueued && j.State == run.Queued
		allTerminal = allTerminal && j.State.Terminal()
	}
	if allQueued {
		return run.Queued
	}
	if !allTerminal {
		return run.Running
	}
	for _, state := range []run.State{run.Failed, run.TimedOut, run.Interrupted, run.Cancelled} {
		for _, j := range jobs {
			if j.State == state {
				return state
			}
		}
	}
	allSkipped := len(jobs) > 0
	for _, j := range jobs {
		allSkipped = allSkipped && j.State == run.Skipped
	}
	if allSkipped {
		return run.Skipped
	}
	return run.Succeeded
}

func (s *Store) ListRuns(ctx context.Context) ([]run.Run, error) {
	return s.listRuns(ctx, false)
}
func (s *Store) ListRunSummaries(ctx context.Context) ([]run.Run, error) {
	return s.listRuns(ctx, true)
}
func (s *Store) listRuns(ctx context.Context, summary bool) ([]run.Run, error) {
	var records []run.Run
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT id FROM runs ORDER BY created_at DESC,id DESC LIMIT 100")
		if err != nil {
			return fmt.Errorf("list runs: %w", err)
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan run ID: %w", err)
			}
			ids = append(ids, id)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return fmt.Errorf("list runs: %w", err)
		}
		for _, id := range ids {
			record, err := getRun(ctx, tx, id, summary)
			if err != nil {
				return err
			}
			records = append(records, record)
		}
		return nil
	})
	return records, err
}

func (s *Store) GetJob(ctx context.Context, id string) (run.Job, error) {
	return getJob(ctx, s.db, id)
}

func getJob(ctx context.Context, q queryer, id string) (run.Job, error) {
	var job run.Job
	var body []byte
	if err := q.QueryRowContext(ctx, "SELECT body FROM jobs WHERE id=?", id).Scan(&body); err != nil {
		return job, fmt.Errorf("get job %s: %w", id, notFound(err))
	}
	if err := json.Unmarshal(body, &job); err != nil {
		return job, fmt.Errorf("decode job: %w", err)
	}
	return job, nil
}

func queryJobs(ctx context.Context, q queryer, query string, args ...any) ([]run.Job, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query jobs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var jobs []run.Job
	for rows.Next() {
		var job run.Job
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, fmt.Errorf("scan job: %w", err)
		}
		if err := json.Unmarshal(body, &job); err != nil {
			return nil, fmt.Errorf("decode job: %w", err)
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func saveJob(ctx context.Context, tx *sql.Tx, job run.Job) error {
	job.UpdatedAt = time.Now().UTC()
	if job.State.Terminal() && job.Progress != nil && len(job.Progress.Phases) > 0 {
		i := len(job.Progress.Phases) - 1
		if job.Progress.Phases[i].FinishedAt.IsZero() {
			end := job.UpdatedAt
			if end.Before(job.Progress.LastAt) {
				end = job.Progress.LastAt
			}
			job.Progress.Phases[i].FinishedAt = end
		}
	}
	body, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("encode job: %w", err)
	}
	// Dirty is preserved when only Pod identity or cancellation intent changes.
	terminalAt := int64(0)
	if job.State.Terminal() {
		terminalAt = job.UpdatedAt.UnixNano()
	}
	r, err := tx.ExecContext(ctx, "UPDATE jobs SET check_dirty=CASE WHEN state<>? THEN 1 ELSE check_dirty END,check_attempts=CASE WHEN state<>? THEN 0 ELSE check_attempts END,check_next_attempt_at=CASE WHEN state<>? THEN 0 ELSE check_next_attempt_at END,state=?,body=?,terminal_at=CASE WHEN terminal_at=0 THEN ? ELSE terminal_at END WHERE id=?", job.State, job.State, job.State, job.State, body, terminalAt, job.ID)
	return affected(r, err, "save job")
}

func (s *Store) Active(ctx context.Context) ([]run.Job, error) {
	return queryJobs(ctx, s.db, "SELECT body FROM jobs WHERE state IN (?,?) ORDER BY created_at,id", run.Starting, run.Running)
}

func (s *Store) RecordPod(ctx context.Context, id, uid string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		job, err := getJob(ctx, tx, id)
		if err != nil {
			return err
		}
		if uid == "" || (job.PodUID != "" && job.PodUID != uid) || job.State == run.Queued || (job.State.Terminal() && job.PodUID == "") {
			return fmt.Errorf("record Pod for %s: %w", id, ErrConflict)
		}
		job.PodUID = uid
		return saveJob(ctx, tx, job)
	})
}

func (s *Store) Claim(ctx context.Context, id, uid string) (run.Plan, error) {
	var plan run.Plan
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		job, err := getJob(ctx, tx, id)
		if err != nil {
			return err
		}
		if uid == "" || (job.PodUID != "" && job.PodUID != uid) || (job.State != run.Starting && job.State != run.Running) || job.CancelRequested {
			return fmt.Errorf("claim job %s: %w", id, ErrConflict)
		}
		job.PodUID, job.State = uid, run.Running
		if job.ClaimedAt.IsZero() {
			job.ClaimedAt = time.Now().UTC()
		}
		if err := saveJob(ctx, tx, job); err != nil {
			return err
		}
		plan = job.Plan
		return nil
	})
	return plan, err
}

func (s *Store) CancelRun(ctx context.Context, id string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		record, err := getRun(ctx, tx, id)
		if err != nil {
			return err
		}
		for _, job := range record.Jobs {
			if job.State.Terminal() {
				continue
			}
			job.CancelRequested = true
			if job.State == run.Queued {
				job.State = run.Cancelled
				job.Result = &run.Result{State: run.Cancelled}
			}
			if err := saveJob(ctx, tx, job); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) Interrupt(ctx context.Context, id, reason string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		job, err := getJob(ctx, tx, id)
		if err != nil {
			return err
		}
		if job.State.Terminal() {
			return nil
		}
		var last int64
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(max(seq),0) FROM logs WHERE job_id=?", id).Scan(&last); err != nil {
			return fmt.Errorf("read final log sequence: %w", err)
		}
		job.State = run.Interrupted
		if job.CancelRequested {
			job.State = run.Cancelled
		}
		job.Result = &run.Result{State: job.State, Error: reason, LastLogSeq: last}
		return saveTerminalJob(ctx, tx, job)
	})
}
