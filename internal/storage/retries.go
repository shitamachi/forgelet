package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/shitamachi/forgelet/internal/run"
)

const RetryLimit = 12

func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 9 {
		return 5 * time.Minute
	}
	d := 2 * time.Second * time.Duration(1<<uint(attempt-1))
	if d > 5*time.Minute {
		d = 5 * time.Minute
	}
	return d
}
func retryMessage(text string) string {
	if len(text) > 2048 {
		return text[:2048]
	}
	return text
}
func retryTime(times []time.Time) time.Time {
	if len(times) > 0 {
		return times[0]
	}
	return time.Now().UTC()
}
func (s *Store) migrateV6(ctx context.Context) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `ALTER TABLE events ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE events ADD COLUMN next_attempt_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN check_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN check_next_attempt_at INTEGER NOT NULL DEFAULT 0;
CREATE INDEX events_retry ON events(done,next_attempt_at,created_at);
CREATE INDEX checks_retry ON jobs(check_dirty,check_next_attempt_at,created_at);
PRAGMA user_version=6;`)
		return err
	})
}
func (s *Store) RetryEvent(ctx context.Context, id, message string, now time.Time) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		var attempts int
		var done int
		if err := tx.QueryRowContext(ctx, "SELECT attempts,done FROM events WHERE delivery_id=?", id).Scan(&attempts, &done); err != nil {
			return notFound(err)
		}
		if done != 0 {
			return nil
		}
		attempts++
		if attempts >= RetryLimit {
			done = 1
			message = "retry limit reached: " + message
		}
		next := now.Add(retryDelay(attempts)).UnixNano()
		_, err := tx.ExecContext(ctx, "UPDATE events SET attempts=?,next_attempt_at=?,error=?,done=? WHERE delivery_id=?", attempts, next, retryMessage(message), done, id)
		return err
	})
}
func (s *Store) RetryCheck(ctx context.Context, id string, state run.State, message string, now time.Time) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		job, err := getJob(ctx, tx, id)
		if err != nil {
			return err
		}
		if job.State != state {
			return nil
		}
		var attempts int
		if err := tx.QueryRowContext(ctx, "SELECT check_attempts FROM jobs WHERE id=?", id).Scan(&attempts); err != nil {
			return err
		}
		attempts++
		dirty := 1
		if attempts >= RetryLimit {
			dirty = 0
			message = "retry limit reached; GitHub Checks delivery paused: " + message
		}
		job.CheckError = retryMessage(message)
		body, err := json.Marshal(job)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE jobs SET check_attempts=?,check_next_attempt_at=?,check_dirty=?,body=? WHERE id=?", attempts, now.Add(retryDelay(attempts)).UnixNano(), dirty, body, id)
		return err
	})
}

// RetryRuntime persists bounded retries across restarts. A permanent error or
// exhausted budget releases capacity without ever pretending the job succeeded.
func (s *Store) RetryRuntime(ctx context.Context, id, message string, permanent bool, now time.Time) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		job, err := getJob(ctx, tx, id)
		if err != nil {
			return err
		}
		if job.State.Terminal() {
			return nil
		}
		job.RuntimeFailures++
		job.RuntimeError = retryMessage(message)
		job.RuntimeRetryAt = now.Add(retryDelay(job.RuntimeFailures))
		if permanent || job.RuntimeFailures >= RetryLimit {
			var last int64
			if err := tx.QueryRowContext(ctx, "SELECT COALESCE(max(seq),0) FROM logs WHERE job_id=?", id).Scan(&last); err != nil {
				return err
			}
			job.State = run.Interrupted
			if job.CancelRequested {
				job.State = run.Cancelled
			}
			job.Result = &run.Result{State: job.State, Error: job.RuntimeError, LastLogSeq: last}
			return saveTerminalJob(ctx, tx, job)
		}
		return saveJob(ctx, tx, job)
	})
}
func (s *Store) ClearRuntimeRetry(ctx context.Context, id string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		job, err := getJob(ctx, tx, id)
		if err != nil {
			return err
		}
		if job.RuntimeFailures == 0 {
			return nil
		}
		job.RuntimeFailures = 0
		job.RuntimeError = ""
		job.RuntimeRetryAt = time.Time{}
		return saveJob(ctx, tx, job)
	})
}

// ReplaceCheck uses the outbox snapshot as a CAS lease when a deleted remote
// check has to be recreated. The ordinary SaveCheck still rejects arbitrary ID swaps.
func (s *Store) ReplaceCheck(ctx context.Context, id string, previous, remote int64, state run.State) error {
	if remote <= 0 || previous <= 0 {
		return fmt.Errorf("invalid remote check identity: %w", ErrConflict)
	}
	return s.saveCheck(ctx, id, remote, state, &previous)
}

func (s *Store) RetryRunChecks(ctx context.Context, id string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		record, err := getRun(ctx, tx, id)
		if err != nil {
			return err
		}
		if record.InstallationID == 0 {
			return fmt.Errorf("run has no GitHub installation: %w", ErrConflict)
		}
		_, err = tx.ExecContext(ctx, "UPDATE jobs SET check_dirty=1,check_attempts=0,check_next_attempt_at=0,body=json_remove(body,'$.check_error') WHERE run_id=?", id)
		return err
	})
}
func (s *Store) RetryFailedEvent(ctx context.Context, id string) error {
	r, err := s.db.ExecContext(ctx, "UPDATE events SET done=0,attempts=0,next_attempt_at=0,error='' WHERE delivery_id=? AND error<>'' AND payload_expired=0", id)
	return affected(r, err, "retry event")
}
