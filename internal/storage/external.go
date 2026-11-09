package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/shitamachi/forgelet/internal/run"
)

func (s *Store) RunnerRegistrations(ctx context.Context) ([]byte, error) {
	var body []byte
	err := s.db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='external_runners'").Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return body, err
}

func (s *Store) SaveRunnerRegistrations(ctx context.Context, body []byte) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES('external_runners',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", body)
	return err
}

func (s *Store) migrateV8(ctx context.Context) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
CREATE UNIQUE INDEX IF NOT EXISTS jobs_external_attempt ON jobs(json_extract(body,'$.attempt_id'));
CREATE INDEX IF NOT EXISTS jobs_external_profile ON jobs(json_extract(body,'$.profile'),state)
 WHERE json_extract(body,'$.plan.external')=1;
PRAGMA user_version=8;`)
		return err
	})
}

type ExternalReservation struct {
	RunnerID, AttemptID string
	Repositories        map[string]bool // Lowercase authorized repository names.
	PodFirst            bool
	Platforms           []string // nil for a client that predates capability reporting.
	Labels              []string // nil for a client that predates capability reporting.
}

type RunnerCapacity struct {
	Platforms []string
	Labels    []string
}

func (r RunnerCapacity) supports(plan run.Plan) bool {
	return !run.DeviceLabelMismatch(plan.Profile, r.Labels) &&
		(plan.ExternalBuild == nil || slices.Contains(r.Platforms, plan.ExternalBuild.Platform))
}

func (r *ExternalReservation) matches(job run.Job) bool {
	if r == nil {
		return !job.Plan.External || job.Plan.FallbackProfile != ""
	}
	return job.Plan.External && r.Repositories[strings.ToLower(job.Plan.Repository)] &&
		!run.DeviceLabelMismatch(job.Plan.Profile, r.Labels) &&
		(job.Plan.FallbackProfile == "" || !r.PodFirst && (RunnerCapacity{Platforms: r.Platforms, Labels: r.Labels}).supports(job.Plan))
}

func (r *ExternalReservation) existing(ctx context.Context, tx *sql.Tx) (run.Job, bool, error) {
	jobs, err := queryJobs(ctx, tx, `SELECT body FROM jobs
WHERE json_extract(body,'$.attempt_id')=?
OR (state IN ('starting','running') AND json_extract(body,'$.plan.external')=1
    AND COALESCE(json_extract(body,'$.runner_id'),json_extract(body,'$.profile'))=?)`,
		r.AttemptID, r.RunnerID)
	if err != nil {
		return run.Job{}, false, err
	}
	for _, job := range jobs {
		if job.AttemptID != r.AttemptID {
			continue
		}
		if !job.Plan.External || job.ExternalRunnerID() != r.RunnerID ||
			!r.Repositories[strings.ToLower(job.Plan.Repository)] || job.CancelRequested ||
			job.State != run.Starting || !time.Now().Before(job.LeaseUntil) {
			return run.Job{}, true, fmt.Errorf("launch grant unavailable: %w", ErrConflict)
		}
		return job, true, nil
	}
	return run.Job{}, len(jobs) > 0, nil
}

// HasQueuedExternal reports whether any queued external job waits for one of
// these device labels. The scheduler asks before a device steps aside for a
// better peer, so the answer must stay cheap: one indexed lookup, no decoding.
func (s *Store) HasQueuedExternal(ctx context.Context, labels []string) (bool, error) {
	if len(labels) == 0 {
		return false, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(labels)), ",")
	args := make([]any, len(labels))
	for i, label := range labels {
		args[i] = label
	}
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM jobs
	WHERE state='queued' AND json_extract(body,'$.plan.external')=1
	AND json_extract(body,'$.profile') IN (`+placeholders+`))`, args...).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("query queued external jobs: %w", err)
	}
	return exists, nil
}

func (s *Store) ReserveExternal(ctx context.Context, limit int, request ExternalReservation) (run.Job, error) {
	jobs, err := s.reserveAuthorized(ctx, limit, request.Repositories, &request, nil)
	if len(jobs) == 0 {
		return run.Job{}, err
	}
	return jobs[0], err
}

func (s *Store) RenewExternal(ctx context.Context, id, attempt string) (run.Job, error) {
	var job run.Job
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var err error
		job, err = getJob(ctx, tx, id)
		if err != nil {
			return err
		}
		if !job.Plan.External || !validExecution(job, attempt) {
			return fmt.Errorf("expired or invalid execution lease: %w", ErrConflict)
		}
		if job.CancelRequested || job.State.Terminal() {
			return nil
		}
		job.LeaseUntil = time.Now().Add(run.ExternalLease).UTC()
		return saveJob(ctx, tx, job)
	})
	return job, err
}

func (s *Store) ExpireExternal(ctx context.Context, id string, now time.Time) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		job, err := getJob(ctx, tx, id)
		if err != nil {
			return err
		}
		if !job.Plan.External || job.State.Terminal() || now.Before(job.LeaseUntil) {
			return nil
		}
		return interruptJob(ctx, tx, job, "external runner lease expired")
	})
}

func canClaim(job run.Job, identity string) bool {
	if job.Plan.External {
		return validExecution(job, identity)
	}
	return identity != "" && (job.PodUID == "" || job.PodUID == identity)
}

func validExecution(job run.Job, identity string) bool {
	if job.Plan.External {
		return identity != "" && job.AttemptID == identity && time.Now().Before(job.LeaseUntil)
	}
	return identity != "" && job.PodUID == identity
}
