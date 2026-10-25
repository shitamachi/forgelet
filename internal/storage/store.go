// Package storage persists events, execution plans and results in a single-writer SQLite database.
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"modernc.org/sqlite"

	"github.com/shitamachi/forgelet/internal/run"
)

var (
	ErrNotFound        = errors.New("not found")
	ErrConflict        = errors.New("conflict")
	ErrLimit           = errors.New("limit exceeded")
	ErrRestoreRequired = errors.New("backup requires explicit restore preparation")
	ErrLogsExpired     = errors.New("logs expired")
)

const (
	MaxLogChunk = 64 << 10
	MaxJobLogs  = 20 << 20
)

type Store struct {
	db       *sql.DB
	lock     *os.File
	close    sync.Once
	closeErr error
}

const schema = `
CREATE TABLE IF NOT EXISTS metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS events (
 delivery_id TEXT PRIMARY KEY, body BLOB NOT NULL, error TEXT NOT NULL DEFAULT '',
 done INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS runs (
 id TEXT PRIMARY KEY, delivery_id TEXT NOT NULL, workflow_path TEXT NOT NULL,
 body BLOB NOT NULL, created_at INTEGER NOT NULL, UNIQUE(delivery_id, workflow_path)
);
CREATE TABLE IF NOT EXISTS jobs (
 id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id), state TEXT NOT NULL,
 body BLOB NOT NULL, created_at INTEGER NOT NULL, log_bytes INTEGER NOT NULL DEFAULT 0,
 check_id INTEGER NOT NULL DEFAULT 0, check_dirty INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS jobs_state ON jobs(state, created_at, id);
CREATE INDEX IF NOT EXISTS jobs_run ON jobs(run_id, created_at, id);
CREATE TABLE IF NOT EXISTS logs (
 job_id TEXT NOT NULL REFERENCES jobs(id), seq INTEGER NOT NULL CHECK(seq > 0),
 step_id TEXT NOT NULL, data TEXT NOT NULL, PRIMARY KEY(job_id, seq)
);
PRAGMA user_version = 1;
`

// Open holds an exclusive filesystem lock until Close. path is a database file,
// not a directory; in-memory databases are deliberately unsupported.
func Open(ctx context.Context, path string) (*Store, error) {
	return openStore(ctx, path, false)
}

func openStore(ctx context.Context, path string, restoring bool) (*Store, error) {
	if path == "" || path == ":memory:" {
		return nil, fmt.Errorf("database path must name a persistent file: %w", ErrConflict)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("database path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	// A directory-wide lock also prevents accidental second databases sharing a data directory.
	lock, err := os.OpenFile(filepath.Join(filepath.Dir(abs), ".forgelet.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open database lock: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("data directory already in use: %w", errors.Join(ErrConflict, err))
	}
	s := &Store{lock: lock}
	fail := func(err error) (*Store, error) { _ = s.Close(); return nil, err }
	if fi, err := os.Lstat(abs); err == nil && !fi.Mode().IsRegular() {
		return fail(fmt.Errorf("database is not a regular file: %w", ErrConflict))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fail(fmt.Errorf("inspect database: %w", err))
	}
	f, err := os.OpenFile(abs, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fail(fmt.Errorf("create database: %w", err))
	}
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return fail(fmt.Errorf("protect database: %w", err))
	}
	if err := f.Close(); err != nil {
		return fail(fmt.Errorf("close database file: %w", err))
	}
	s.db, err = sql.Open("sqlite", abs)
	if err != nil {
		return fail(fmt.Errorf("open sqlite: %w", err))
	}
	s.db.SetMaxOpenConns(1)
	s.db.SetMaxIdleConns(1)
	// Setting auto_vacuum before the first table enables bounded reclamation for
	// new databases. Existing v1 databases retain their mode and reuse free pages;
	// upgrading never performs a blocking full VACUUM.
	if _, err := s.db.ExecContext(ctx, "PRAGMA auto_vacuum=INCREMENTAL; PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;"); err != nil {
		return fail(fmt.Errorf("configure sqlite: %w", err))
	}
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fail(fmt.Errorf("read schema version: %w", err))
	}
	if version > 6 {
		return fail(fmt.Errorf("unsupported schema version %d: %w", version, ErrConflict))
	}
	if version == 0 {
		if _, err := s.db.ExecContext(ctx, schema); err != nil {
			return fail(fmt.Errorf("initialize sqlite: %w", err))
		}
	}
	if version < 2 {
		if err := s.migrateV2(ctx); err != nil {
			return fail(err)
		}
	}
	if version < 3 {
		if err := s.transaction(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `CREATE TABLE settings(repository TEXT NOT NULL,kind TEXT NOT NULL,name TEXT NOT NULL,value BLOB NOT NULL,PRIMARY KEY(repository,kind,name)); PRAGMA user_version=3;`)
			return err
		}); err != nil {
			return fail(fmt.Errorf("migrate settings: %w", err))
		}
	}

	if version < 4 {
		if err := s.transaction(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `CREATE TABLE deliveries(id TEXT PRIMARY KEY,run_id TEXT NOT NULL,target TEXT NOT NULL,state TEXT NOT NULL,version INTEGER NOT NULL,created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL,body BLOB NOT NULL);
CREATE INDEX deliveries_state ON deliveries(state,created_at);
CREATE INDEX deliveries_run ON deliveries(run_id,state);
PRAGMA user_version=4;`)
			return err
		}); err != nil {
			return fail(fmt.Errorf("migrate deliveries: %w", err))
		}
	}
	var required int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM metadata WHERE key='restore_required'").Scan(&required); err != nil {
		return fail(fmt.Errorf("read restore status: %w", err))
	}
	if required > 0 && !restoring {
		return fail(ErrRestoreRequired)
	}
	if required == 0 && restoring {
		return fail(fmt.Errorf("database is not a marked backup: %w", ErrConflict))
	}
	if version < 5 {
		if err := s.migrateV5(ctx); err != nil {
			return fail(err)
		}
	}
	if version < 6 {
		if err := s.migrateV6(ctx); err != nil {
			return fail(fmt.Errorf("migrate retry schema: %w", err))
		}
	}
	return s, nil
}

func (s *Store) Close() error {
	s.close.Do(func() {
		if s.db != nil {
			s.closeErr = s.db.Close()
		}
		if s.lock != nil {
			s.closeErr = errors.Join(s.closeErr, s.lock.Close())
		}
	})
	return s.closeErr
}

func (s *Store) PutEvent(ctx context.Context, event run.Event) (bool, error) {
	if event.DeliveryID == "" {
		return false, fmt.Errorf("empty delivery ID: %w", ErrConflict)
	}
	body, err := json.Marshal(event)
	if err != nil {
		return false, fmt.Errorf("encode event: %w", err)
	}
	r, err := s.db.ExecContext(ctx, "INSERT INTO events(delivery_id,body,created_at) VALUES(?,?,?) ON CONFLICT(delivery_id) DO NOTHING", event.DeliveryID, body, time.Now().UnixNano())
	if err != nil {
		return false, fmt.Errorf("persist event: %w", err)
	}
	n, err := r.RowsAffected()
	return n == 1, err
}

func (s *Store) PendingEvents(ctx context.Context, times ...time.Time) ([]run.Event, error) {
	return s.events(ctx, "SELECT body,error FROM events WHERE done=0 AND next_attempt_at<=? ORDER BY next_attempt_at,created_at,delivery_id LIMIT 100", retryTime(times).UnixNano())
}

// Events includes processed events and their compilation/ingestion diagnostics.
func (s *Store) Events(ctx context.Context) ([]run.Event, error) {
	return s.events(ctx, "SELECT body,error FROM events ORDER BY created_at DESC,delivery_id DESC LIMIT 100")
}

func (s *Store) events(ctx context.Context, query string, args ...any) ([]run.Event, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("pending events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var events []run.Event
	for rows.Next() {
		var body []byte
		var event run.Event
		var errText string
		if err := rows.Scan(&body, &errText); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		if err := json.Unmarshal(body, &event); err != nil {
			return nil, fmt.Errorf("decode event: %w", err)
		}
		event.Error = errText
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) FinishEvent(ctx context.Context, deliveryID, errorText string) error {
	r, err := s.db.ExecContext(ctx, "UPDATE events SET done=1,error=? WHERE delivery_id=?", errorText, deliveryID)
	return affected(r, err, "finish event")
}

func affected(r sql.Result, err error, operation string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	if n == 0 {
		return fmt.Errorf("%s: %w", operation, ErrNotFound)
	}
	return nil
}

func (s *Store) transaction(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func databaseError(err error) error {
	var sqlErr *sqlite.Error
	// Extended constraint codes keep SQLITE_CONSTRAINT (19) in the low byte.
	if errors.As(err, &sqlErr) && sqlErr.Code()&0xff == 19 {
		return errors.Join(ErrConflict, err)
	}
	return err
}

// Backup writes a consistent SQLite snapshot. It never overwrites a destination.
// Restoration is an operator procedure; it must fence old instances and must not
// automatically resume unfinished attempts from an older snapshot.
func (s *Store) Backup(ctx context.Context, destination string) error {
	abs, err := filepath.Abs(destination)
	if err != nil {
		return fmt.Errorf("backup destination: %w", err)
	}
	if _, err := os.Lstat(abs); err == nil {
		return fmt.Errorf("backup destination exists: %w", os.ErrExist)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect backup destination: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(abs), ".forgelet-backup-*")
	if err != nil {
		return fmt.Errorf("create backup staging file: %w", err)
	}
	temporary := f.Name()
	defer func() { _ = os.Remove(temporary) }()
	if err := f.Close(); err != nil {
		return fmt.Errorf("close backup destination: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", temporary); err != nil {
		return fmt.Errorf("backup sqlite: %w", err)
	}
	// Mark only the copy, and publish it only after the marker is durable. A
	// crash during backup must never expose an unmarked, resumable snapshot.
	copyDB, err := sql.Open("sqlite", temporary)
	if err != nil {
		return fmt.Errorf("open snapshot: %w", err)
	}
	_, markerErr := copyDB.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES('restore_required','1') ON CONFLICT(key) DO UPDATE SET value='1'")
	if err := errors.Join(markerErr, copyDB.Close()); err != nil {
		return fmt.Errorf("mark snapshot for restore: %w", err)
	}
	// VACUUM's output must be synced before an operator moves it off the node.
	f, err = os.OpenFile(temporary, os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("open backup for sync: %w", err)
	}
	err = f.Sync()
	if err := errors.Join(err, f.Close()); err != nil {
		return fmt.Errorf("sync backup: %w", err)
	}
	// Hard-link publication is atomic and fails if the destination appeared
	// concurrently. Both paths are in the same directory/filesystem.
	if err := os.Link(temporary, abs); err != nil {
		return fmt.Errorf("publish backup: %w", err)
	}
	dir, err := os.Open(filepath.Dir(abs))
	if err != nil {
		return fmt.Errorf("open backup directory: %w", err)
	}
	err = dir.Sync()
	return errors.Join(err, dir.Close())
}

// PrepareRestore explicitly quarantines all unfinished work in a marked backup.
// The caller must first fence the old server and account for its execution Pods.
// Preparation does not start a server or replay events; a later Open may proceed.
func PrepareRestore(ctx context.Context, path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("inspect restore database: %w", err)
	}
	s, err := openStore(ctx, path, true)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	return s.transaction(ctx, func(tx *sql.Tx) error {
		jobs, err := queryJobs(ctx, tx, "SELECT body FROM jobs WHERE state IN (?,?,?)", run.Queued, run.Starting, run.Running)
		if err != nil {
			return err
		}
		for _, job := range jobs {
			var last int64
			if err := tx.QueryRowContext(ctx, "SELECT COALESCE(max(seq),0) FROM logs WHERE job_id=?", job.ID).Scan(&last); err != nil {
				return fmt.Errorf("read restored log sequence: %w", err)
			}
			job.State = run.Interrupted
			job.Result = &run.Result{State: run.Interrupted, Error: "quarantined after backup restore; explicitly rerun if safe", LastLogSeq: last}
			if err := saveJob(ctx, tx, job); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE events SET done=1,error='quarantined after backup restore; explicitly redeliver if safe' WHERE done=0"); err != nil {
			return fmt.Errorf("quarantine restored events: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE deliveries SET state='cancelled',version=version+1,body=json_set(body,'$.state','cancelled','$.version',version+1,'$.error','quarantined after backup restore; inspect remote branches and PRs before redelivery') WHERE state IN ('queued','preparing','awaiting_merge','delivered','deployed')`); err != nil {
			return fmt.Errorf("quarantine restored deliveries: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM metadata WHERE key='restore_required'"); err != nil {
			return fmt.Errorf("complete restore preparation: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM auth_sessions; DELETE FROM auth_flows;"); err != nil {
			return fmt.Errorf("revoke restored authentication: %w", err)
		}

		return nil
	})
}
