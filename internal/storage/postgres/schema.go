// Package postgres implements the durable store port (spec 0002) on
// PostgreSQL: unique delivery keys, atomic run creation, serialized
// claims, monotonic projection. The in-memory adapter's protocol tests
// define the semantics this implementation mirrors.
package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const schema = `
CREATE TABLE IF NOT EXISTS webhook_deliveries (
  provider    TEXT NOT NULL,
  delivery_id TEXT NOT NULL,
  event       JSONB NOT NULL,
  payload     BYTEA NOT NULL,
  run_id      TEXT,
  received_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (provider, delivery_id)
);

CREATE TABLE IF NOT EXISTS workflow_runs (
  id                TEXT PRIMARY KEY,
  delivery_provider TEXT NOT NULL,
  delivery_id       TEXT NOT NULL UNIQUE,
  status            TEXT NOT NULL,
  event             JSONB NOT NULL,
  created_at        TIMESTAMPTZ NOT NULL,
  finished_at       TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS job_runs (
  id                  TEXT PRIMARY KEY,
  run_id              TEXT NOT NULL REFERENCES workflow_runs(id),
  job_key             TEXT NOT NULL,
  runner_class        TEXT NOT NULL,
  depends_on          TEXT[] NOT NULL DEFAULT '{}',
  matrix              JSONB,
  plan_digest         TEXT NOT NULL DEFAULT '',
  condition           TEXT NOT NULL DEFAULT '',
  status              TEXT NOT NULL,
  attempt             INT NOT NULL DEFAULT 1,
  active_name         TEXT NOT NULL DEFAULT '',
  active_uid          TEXT NOT NULL DEFAULT '',
  claimed_at          TIMESTAMPTZ,
  created_at          TIMESTAMPTZ NOT NULL,
  dispatched_at       TIMESTAMPTZ,
  started_at          TIMESTAMPTZ,
  finished_at         TIMESTAMPTZ,
  active_collected_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS job_runs_run_id ON job_runs(run_id);
CREATE INDEX IF NOT EXISTS job_runs_claim ON job_runs(status, created_at) WHERE status = 'queued';

CREATE TABLE IF NOT EXISTS secrets (
  scope              TEXT NOT NULL,
  name               TEXT NOT NULL,
  nonce              BYTEA NOT NULL,
  ciphertext         BYTEA NOT NULL,
  wrapped_dek        BYTEA NOT NULL,
  master_key_version INT  NOT NULL,
  created_at         TIMESTAMPTZ NOT NULL,
  updated_at         TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (scope, name)
);
`

// queryer is satisfied by both *pgxpool.Pool and pgx.Tx.
type queryer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Migrate applies the schema idempotently.
func Migrate(ctx context.Context, db queryer) error {
	if _, err := db.Exec(ctx, schema); err != nil {
		return fmt.Errorf("postgres migrate: %w", err)
	}
	// Backfill for DBs created before the condition column existed.
	if _, err := db.Exec(ctx, `ALTER TABLE job_runs ADD COLUMN IF NOT EXISTS condition TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("postgres migrate condition: %w", err)
	}
	return nil
}
