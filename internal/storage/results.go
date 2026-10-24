package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/shitamachi/forgelet/internal/run"
)

func (s *Store) AppendLog(ctx context.Context, id, uid string, entry run.Log) error {
	if entry.Seq < 1 || len(entry.Data) > MaxLogChunk || len(entry.StepID) > 256 {
		return fmt.Errorf("invalid log sequence or chunk size: %w", ErrLimit)
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		job, err := getJob(ctx, tx, id)
		if err != nil {
			return err
		}
		if uid == "" || job.PodUID != uid {
			return fmt.Errorf("log Pod identity: %w", ErrConflict)
		}
		var old run.Log
		err = tx.QueryRowContext(ctx, "SELECT seq,step_id,data FROM logs WHERE job_id=? AND seq=?", id, entry.Seq).Scan(&old.Seq, &old.StepID, &old.Data)
		if err == nil {
			if old != entry {
				return fmt.Errorf("log sequence %d was already written with different content: %w", entry.Seq, ErrConflict)
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read existing log: %w", err)
		}
		if job.State != run.Running {
			return fmt.Errorf("logs require a running attempt: %w", ErrConflict)
		}
		var last int64
		var size int
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(max(seq),0) FROM logs WHERE job_id=?", id).Scan(&last); err != nil {
			return fmt.Errorf("get log sequence: %w", err)
		}
		if entry.Seq != last+1 {
			return fmt.Errorf("expected log sequence %d, got %d: %w", last+1, entry.Seq, ErrConflict)
		}
		if err := tx.QueryRowContext(ctx, "SELECT log_bytes FROM jobs WHERE id=?", id).Scan(&size); err != nil {
			return fmt.Errorf("read log size: %w", err)
		}
		// Include per-record overhead so empty chunks cannot create an unbounded database.
		cost := len(entry.Data) + len(entry.StepID) + 64
		if size+cost > MaxJobLogs {
			return fmt.Errorf("job log quota: %w", ErrLimit)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO logs(job_id,seq,step_id,data) VALUES(?,?,?,?)", id, entry.Seq, entry.StepID, entry.Data); err != nil {
			return fmt.Errorf("append log: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE jobs SET log_bytes=log_bytes+? WHERE id=?", cost, id); err != nil {
			return fmt.Errorf("account log size: %w", err)
		}
		return nil
	})
}

func (s *Store) Logs(ctx context.Context, id string, after int64) ([]run.Log, error) {
	var entries []run.Log
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		job, err := getJob(ctx, tx, id)
		if err != nil {
			return err
		}
		if job.LogsExpired {
			return ErrLogsExpired
		}
		rows, err := tx.QueryContext(ctx, "SELECT seq,step_id,data FROM logs WHERE job_id=? AND seq>? ORDER BY seq LIMIT 256", id, after)
		if err != nil {
			return fmt.Errorf("read logs: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var entry run.Log
			if err := rows.Scan(&entry.Seq, &entry.StepID, &entry.Data); err != nil {
				return fmt.Errorf("scan log: %w", err)
			}
			entries = append(entries, entry)
		}
		return rows.Err()
	})
	return entries, err
}

func (s *Store) Finish(ctx context.Context, id, uid string, result run.Result) error {
	switch result.State {
	case run.Succeeded, run.Failed, run.Cancelled, run.TimedOut:
	default:
		return fmt.Errorf("executor cannot report state %s: %w", result.State, ErrConflict)
	}
	if err := validateResult(result); err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		job, err := getJob(ctx, tx, id)
		if err != nil {
			return err
		}
		if uid == "" || job.PodUID != uid {
			return fmt.Errorf("result Pod identity: %w", ErrConflict)
		}
		if job.State.Terminal() {
			old, err := json.Marshal(job.Result)
			if err != nil {
				return fmt.Errorf("encode old result: %w", err)
			}
			next, err := json.Marshal(result)
			if err != nil {
				return fmt.Errorf("encode result: %w", err)
			}
			if bytes.Equal(old, next) {
				return nil
			}
			return fmt.Errorf("terminal result cannot change: %w", ErrConflict)
		}
		if job.State != run.Running {
			return fmt.Errorf("only a claimed attempt can finish: %w", ErrConflict)
		}
		var last int64
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(max(seq),0) FROM logs WHERE job_id=?", id).Scan(&last); err != nil {
			return fmt.Errorf("read last log sequence: %w", err)
		}
		if result.LastLogSeq != last {
			return fmt.Errorf("result log sequence %d differs from stored %d: %w", result.LastLogSeq, last, ErrConflict)
		}
		job.State, job.Result = result.State, &result
		if job.Plan.ContinueOnError && result.State == run.Failed && result.Error == "" {
			job.State = run.Succeeded
		}
		return saveTerminalJob(ctx, tx, job)
	})
}

func validateResult(result run.Result) error {
	if len(result.Steps) > 256 || len(result.Error) > 64<<10 {
		return fmt.Errorf("result size: %w", ErrLimit)
	}
	outputsSize := func(outputs map[string]string) (int, error) {
		if len(outputs) > 100 {
			return 0, fmt.Errorf("at most 100 outputs: %w", ErrLimit)
		}
		size := 0
		for key, value := range outputs {
			size += len(key) + len(value)
		}
		if size > 64<<10 {
			return 0, fmt.Errorf("outputs exceed 64 KiB: %w", ErrLimit)
		}
		return size, nil
	}
	if _, err := outputsSize(result.Outputs); err != nil {
		return err
	}
	stepBytes := 0
	for _, step := range result.Steps {
		if len(step.ID) > 256 {
			return fmt.Errorf("step ID length: %w", ErrLimit)
		}
		for _, state := range []run.State{step.State, step.Outcome} {
			switch state {
			case "", run.Succeeded, run.Failed, run.Skipped, run.Cancelled, run.TimedOut:
			default:
				return fmt.Errorf("invalid step state %q: %w", state, ErrConflict)
			}
		}
		size, err := outputsSize(step.Outputs)
		if err != nil {
			return err
		}
		stepBytes += size
	}
	if stepBytes > 64<<10 {
		return fmt.Errorf("step outputs exceed 64 KiB: %w", ErrLimit)
	}
	return nil
}

func (s *Store) PendingChecks(ctx context.Context) ([]run.Check, error) {
	var checks []run.Check
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT jobs.id,check_id FROM jobs JOIN runs ON runs.id=jobs.run_id WHERE check_dirty=1 AND runs.installation_id>0 ORDER BY jobs.created_at,jobs.id LIMIT 100")
		if err != nil {
			return fmt.Errorf("pending checks: %w", err)
		}
		type pending struct {
			id     string
			remote int64
		}
		var pendingJobs []pending
		for rows.Next() {
			var p pending
			if err := rows.Scan(&p.id, &p.remote); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan check: %w", err)
			}
			pendingJobs = append(pendingJobs, p)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return fmt.Errorf("pending check rows: %w", err)
		}
		for _, p := range pendingJobs {
			job, err := getJob(ctx, tx, p.id)
			if err != nil {
				return err
			}
			record, err := getRun(ctx, tx, job.RunID)
			if err != nil {
				return err
			}
			checks = append(checks, run.Check{Job: job, Run: record, RemoteID: p.remote})
		}
		return nil
	})
	return checks, err
}

func (s *Store) SaveCheck(ctx context.Context, id string, remoteID int64, state run.State) error {
	if remoteID <= 0 {
		return fmt.Errorf("invalid remote check ID: %w", ErrConflict)
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		var existing int64
		if err := tx.QueryRowContext(ctx, "SELECT check_id FROM jobs WHERE id=?", id).Scan(&existing); err != nil {
			return fmt.Errorf("get remote check ID: %w", notFound(err))
		}
		if existing != 0 && existing != remoteID {
			return fmt.Errorf("remote check ID already assigned: %w", ErrConflict)
		}
		r, err := tx.ExecContext(ctx, "UPDATE jobs SET check_id=?,check_dirty=CASE WHEN state=? THEN 0 ELSE 1 END WHERE id=?", remoteID, state, id)
		return affected(r, err, "save check")
	})
}
