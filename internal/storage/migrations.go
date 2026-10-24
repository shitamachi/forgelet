package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// v2 keeps v1's JSON records intact while adding indexes for retention and
// lightweight deduplication records that outlive the full execution history.
func (s *Store) migrateV2(ctx context.Context) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
ALTER TABLE jobs ADD COLUMN terminal_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN logs_expired INTEGER NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN installation_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE events ADD COLUMN payload_expired INTEGER NOT NULL DEFAULT 0;
CREATE TABLE run_tombstones (
 id TEXT PRIMARY KEY, delivery_id TEXT NOT NULL, workflow_path TEXT NOT NULL,
 UNIQUE(delivery_id, workflow_path)
);
CREATE INDEX jobs_retention ON jobs(terminal_at,run_id);
CREATE INDEX events_retention ON events(done,payload_expired,created_at);
CREATE INDEX runs_queue_order ON runs(created_at,id);
UPDATE runs SET installation_id=COALESCE(json_extract(body,'$.installation_id'),0);
`)
		if err != nil {
			return fmt.Errorf("migrate retention schema: %w", err)
		}
		jobs, err := queryJobs(ctx, tx, "SELECT body FROM jobs WHERE state NOT IN ('queued','starting','running')")
		if err != nil {
			return err
		}
		for _, job := range jobs {
			finished := job.UpdatedAt
			if finished.IsZero() {
				finished = job.CreatedAt
			}
			if finished.IsZero() {
				finished = time.Now().UTC()
			}
			if _, err := tx.ExecContext(ctx, "UPDATE jobs SET terminal_at=? WHERE id=?", finished.UnixNano(), job.ID); err != nil {
				return fmt.Errorf("migrate terminal time: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, "PRAGMA user_version=2"); err != nil {
			return fmt.Errorf("save schema version: %w", err)
		}
		return nil
	})
}
