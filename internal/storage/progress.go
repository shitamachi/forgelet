package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/shitamachi/forgelet/internal/run"
)

func (s *Store) migrateV7(ctx context.Context) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE TABLE progress_events(job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,seq INTEGER NOT NULL,digest BLOB NOT NULL,PRIMARY KEY(job_id,seq)); PRAGMA user_version=7;`)
		return err
	})
}

// AppendProgress journals bounded digests for exact replay detection, while the
// small materialized view lives with the job. Both update in one transaction.
func (s *Store) AppendProgress(ctx context.Context, id, uid string, e run.ProgressEvent) error {
	if e.Seq < 1 || e.Seq > run.MaxProgressEvents || len(e.Message) > 1024 || len(e.StepID) > 256 {
		return fmt.Errorf("progress size/sequence: %w", ErrLimit)
	}
	body, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode progress: %w", err)
	}
	digest := sha256.Sum256(body)
	return s.transaction(ctx, func(tx *sql.Tx) error {
		job, err := getJob(ctx, tx, id)
		if err != nil {
			return err
		}
		if uid == "" || job.PodUID != uid {
			return fmt.Errorf("progress Pod identity: %w", ErrConflict)
		}
		var old []byte
		err = tx.QueryRowContext(ctx, "SELECT digest FROM progress_events WHERE job_id=? AND seq=?", id, e.Seq).Scan(&old)
		if err == nil {
			if string(old) != string(digest[:]) {
				return fmt.Errorf("progress sequence content changed: %w", ErrConflict)
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read progress digest: %w", err)
		}
		if job.State != run.Running {
			return fmt.Errorf("progress requires claimed running job: %w", ErrConflict)
		}
		if e.At.After(time.Now().Add(5 * time.Minute)) {
			return fmt.Errorf("progress timestamp in future: %w", ErrConflict)
		}
		var prior run.ExecutionProgress
		if job.Progress != nil {
			prior = *job.Progress
		}
		next, err := run.ApplyProgress(prior, e, job.Plan.Steps)
		if err != nil {
			return fmt.Errorf("apply progress: %w: %w", err, ErrConflict)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO progress_events(job_id,seq,digest) VALUES(?,?,?)", id, e.Seq, digest[:]); err != nil {
			return fmt.Errorf("journal progress: %w", err)
		}
		job.Progress = &next
		return saveJob(ctx, tx, job)
	})
}
