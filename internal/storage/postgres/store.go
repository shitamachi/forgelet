package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/run/scheduler"
)

// claimLease bounds how long a crashed dispatcher holds a claim before the
// job becomes claimable again.
const claimLease = 5 * time.Minute

// Store implements scheduler.DurableStore on PostgreSQL.
type Store struct {
	pool *pgxpool.Pool
	ids  scheduler.IDSource
	now  scheduler.Clock
}

// New opens a pool, migrates the schema and returns a Store.
func New(ctx context.Context, databaseURL string, ids scheduler.IDSource, now scheduler.Clock) (*Store, error) {
	if ids == nil {
		return nil, errors.New("postgres: id source required")
	}
	if now == nil {
		now = time.Now
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("postgres: pool: %w", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool, ids: ids, now: now}, nil
}

// Close releases the pool.
func (s *Store) Close() { s.pool.Close() }

// RecordDelivery implements scheduler.DurableStore: first write wins.
func (s *Store) RecordDelivery(ctx context.Context, d model.Delivery) (model.DeliveryRecord, bool, error) {
	ev, err := json.Marshal(d.Event)
	if err != nil {
		return model.DeliveryRecord{}, false, fmt.Errorf("postgres: encode event: %w", err)
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO webhook_deliveries (provider, delivery_id, event, payload, run_id, received_at)
		VALUES ($1, $2, $3, $4, NULL, $5)
		ON CONFLICT (provider, delivery_id) DO NOTHING`,
		d.Key.Provider, d.Key.DeliveryID, ev, d.Payload, s.now())
	if err != nil {
		return model.DeliveryRecord{}, false, fmt.Errorf("postgres: record delivery: %w", err)
	}
	rec, err := s.delivery(ctx, d.Key)
	if err != nil {
		return model.DeliveryRecord{}, false, err
	}
	// CommandTag.Insert() only checks the verb ("INSERT 0 0" counts too),
	// so row count is the reliable conflict signal.
	return rec, tag.RowsAffected() == 1, nil
}

func (s *Store) delivery(ctx context.Context, key model.DeliveryKey) (model.DeliveryRecord, error) {
	var (
		rec       model.DeliveryRecord
		eventJSON []byte
		runID     *string
	)
	err := s.pool.QueryRow(ctx, `
		SELECT provider, delivery_id, event, payload, run_id, received_at
		FROM webhook_deliveries WHERE provider=$1 AND delivery_id=$2`,
		key.Provider, key.DeliveryID).
		Scan(&rec.Key.Provider, &rec.Key.DeliveryID, &eventJSON, &rec.Payload, &runID, &rec.ReceivedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.DeliveryRecord{}, fmt.Errorf("postgres: delivery %s vanished", key)
	}
	if err != nil {
		return model.DeliveryRecord{}, fmt.Errorf("postgres: load delivery: %w", err)
	}
	if err := json.Unmarshal(eventJSON, &rec.Event); err != nil {
		return model.DeliveryRecord{}, fmt.Errorf("postgres: decode event: %w", err)
	}
	if runID != nil {
		rec.RunID = model.RunID(*runID)
	}
	return rec, nil
}

// CreateRun implements scheduler.DurableStore: atomic run+jobs keyed by
// delivery (create-or-get).
func (s *Store) CreateRun(ctx context.Context, seed model.RunSeed, now time.Time) (model.RunRecord, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return model.RunRecord{}, false, fmt.Errorf("postgres: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var existing *string
	err = tx.QueryRow(ctx, `SELECT run_id FROM webhook_deliveries WHERE provider=$1 AND delivery_id=$2`,
		seed.Delivery.Provider, seed.Delivery.DeliveryID).Scan(&existing)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return model.RunRecord{}, false, fmt.Errorf("postgres: delivery %s not recorded", seed.Delivery)
	case err != nil:
		return model.RunRecord{}, false, fmt.Errorf("postgres: load delivery: %w", err)
	}
	if existing != nil && *existing != "" {
		run, rerr := s.runIn(ctx, tx, model.RunID(*existing))
		if rerr != nil {
			return model.RunRecord{}, false, rerr
		}
		if err := tx.Commit(ctx); err != nil {
			return model.RunRecord{}, false, fmt.Errorf("postgres: commit: %w", err)
		}
		return run, false, nil
	}
	if len(seed.Jobs) == 0 {
		return model.RunRecord{}, false, fmt.Errorf("postgres: run for delivery %s has no jobs", seed.Delivery)
	}

	runID := s.ids.NewRunID()
	eventJSON, err := json.Marshal(seed.Event)
	if err != nil {
		return model.RunRecord{}, false, fmt.Errorf("postgres: encode event: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO workflow_runs (id, delivery_provider, delivery_id, status, event, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		runID, seed.Delivery.Provider, seed.Delivery.DeliveryID, model.RunQueued, eventJSON, now); err != nil {
		return model.RunRecord{}, false, fmt.Errorf("postgres: insert run: %w", err)
	}

	for _, intent := range seed.Jobs {
		jobID := s.ids.NewJobRunID()
		dependsOn := intent.DependsOn
		if dependsOn == nil {
			dependsOn = []string{} // pgx encodes a nil slice as NULL
		}
		var matrixJSON any
		if intent.Matrix != nil {
			m, merr := json.Marshal(intent.Matrix)
			if merr != nil {
				return model.RunRecord{}, false, fmt.Errorf("postgres: encode matrix: %w", merr)
			}
			matrixJSON = m
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO job_runs (id, run_id, job_key, runner_class, depends_on, matrix, plan_digest, status, attempt, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 1, $9)`,
			jobID, runID, intent.JobKey, intent.RunnerClass, dependsOn, matrixJSON,
			intent.PlanDigest, model.JobQueued, now); err != nil {
			return model.RunRecord{}, false, fmt.Errorf("postgres: insert job: %w", err)
		}
	}

	if _, err := tx.Exec(ctx, `UPDATE webhook_deliveries SET run_id=$1 WHERE provider=$2 AND delivery_id=$3 AND run_id IS NULL`,
		runID, seed.Delivery.Provider, seed.Delivery.DeliveryID); err != nil {
		return model.RunRecord{}, false, fmt.Errorf("postgres: bind delivery: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return model.RunRecord{}, false, fmt.Errorf("postgres: commit: %w", err)
	}
	return model.RunRecord{ID: runID, Event: seed.Event, Delivery: seed.Delivery, Status: model.RunQueued, CreatedAt: now}, true, nil
}

// GetRun implements scheduler.DurableStore.
func (s *Store) GetRun(ctx context.Context, id model.RunID) (model.RunRecord, error) {
	return s.runIn(ctx, s.pool, id)
}

func (s *Store) runIn(ctx context.Context, q queryer, id model.RunID) (model.RunRecord, error) {
	var (
		run       model.RunRecord
		eventJSON []byte
	)
	err := q.QueryRow(ctx, `
		SELECT id, delivery_provider, delivery_id, status, event, created_at, finished_at
		FROM workflow_runs WHERE id=$1`, id).
		Scan(&run.ID, &run.Delivery.Provider, &run.Delivery.DeliveryID, &run.Status, &eventJSON, &run.CreatedAt, &run.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.RunRecord{}, scheduler.ErrRunNotFound
	}
	if err != nil {
		return model.RunRecord{}, fmt.Errorf("postgres: load run: %w", err)
	}
	if err := json.Unmarshal(eventJSON, &run.Event); err != nil {
		return model.RunRecord{}, fmt.Errorf("postgres: decode run event: %w", err)
	}
	return run, nil
}

// GetJobRun implements scheduler.DurableStore.
func (s *Store) GetJobRun(ctx context.Context, id model.JobRunID) (model.JobRunRecord, error) {
	return s.jobIn(ctx, s.pool, id)
}

func (s *Store) jobIn(ctx context.Context, q queryer, id model.JobRunID) (model.JobRunRecord, error) {
	rows, err := q.Query(ctx, jobCols+` WHERE id=$1`, id)
	if err != nil {
		return model.JobRunRecord{}, fmt.Errorf("postgres: load job: %w", err)
	}
	job, err := pgx.CollectOneRow(rows, scanJob)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.JobRunRecord{}, scheduler.ErrJobRunNotFound
	}
	if err != nil {
		return model.JobRunRecord{}, fmt.Errorf("postgres: load job: %w", err)
	}
	return job, nil
}

// ListJobRuns implements scheduler.DurableStore.
func (s *Store) ListJobRuns(ctx context.Context, run model.RunID) ([]model.JobRunRecord, error) {
	if _, err := s.GetRun(ctx, run); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, jobCols+` WHERE run_id=$1 ORDER BY created_at, id`, run)
	if err != nil {
		return nil, fmt.Errorf("postgres: list jobs: %w", err)
	}
	jobs, err := pgx.CollectRows(rows, scanJob)
	if err != nil {
		return nil, fmt.Errorf("postgres: list jobs: %w", err)
	}
	return jobs, nil
}

const jobCols = `SELECT id, run_id, job_key, runner_class, depends_on, matrix, plan_digest,
	status, attempt, active_name, active_uid, created_at, dispatched_at, started_at, finished_at, active_collected_at
	FROM job_runs`

func scanJob(row pgx.CollectableRow) (model.JobRunRecord, error) {
	var (
		j          model.JobRunRecord
		matrixJSON []byte
	)
	err := row.Scan(&j.ID, &j.RunID, &j.JobKey, &j.RunnerClass, &j.DependsOn, &matrixJSON, &j.PlanDigest,
		&j.Status, &j.Attempt, &j.ActiveName, &j.ActiveUID,
		&j.CreatedAt, &j.DispatchedAt, &j.StartedAt, &j.FinishedAt, &j.ActiveCollectedAt)
	if err != nil {
		return model.JobRunRecord{}, err
	}
	if matrixJSON != nil {
		if err := json.Unmarshal(matrixJSON, &j.Matrix); err != nil {
			return model.JobRunRecord{}, fmt.Errorf("decode matrix: %w", err)
		}
	}
	return j, nil
}

// ClaimNextQueuedJob implements scheduler.DurableStore: a leased claim in a
// short transaction, with dependency gating and skip propagation mirroring
// the memory adapter.
func (s *Store) ClaimNextQueuedJob(ctx context.Context) (model.JobRunRecord, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return model.JobRunRecord{}, fmt.Errorf("postgres: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	rows, err := tx.Query(ctx, `
		SELECT id FROM job_runs
		WHERE status='queued'
		  AND (claimed_at IS NULL OR claimed_at < $1::timestamptz - make_interval(secs => $2::double precision / 1000000000))
		ORDER BY created_at, id
		FOR UPDATE SKIP LOCKED`, s.now(), float64(claimLease.Nanoseconds()))
	if err != nil {
		return model.JobRunRecord{}, fmt.Errorf("postgres: claim select: %w", err)
	}
	ids, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.JobRunID, error) {
		var id model.JobRunID
		return id, r.Scan(&id)
	})
	if err != nil {
		return model.JobRunRecord{}, fmt.Errorf("postgres: claim ids: %w", err)
	}

	now := s.now()
	for _, id := range ids {
		switch s.depState(ctx, tx, id) {
		case depsBlocked:
			continue
		case depsFailed:
			if _, err := tx.Exec(ctx, `
				UPDATE job_runs SET status=$2, finished_at=$3 WHERE id=$1 AND status='queued'`,
				id, model.JobSkipped, now); err != nil {
				return model.JobRunRecord{}, fmt.Errorf("postgres: skip job: %w", err)
			}
			if err := s.refreshRun(ctx, tx, id, now); err != nil {
				return model.JobRunRecord{}, err
			}
			continue
		}
		var claimed model.JobRunID
		err := tx.QueryRow(ctx, `
			UPDATE job_runs SET claimed_at=$2 WHERE id=$1 AND status='queued'
			RETURNING id`, id, now).Scan(&claimed)
		if errors.Is(err, pgx.ErrNoRows) {
			continue // raced or swept
		}
		if err != nil {
			return model.JobRunRecord{}, fmt.Errorf("postgres: claim: %w", err)
		}
		job, jerr := s.jobIn(ctx, tx, claimed)
		if jerr != nil {
			return model.JobRunRecord{}, jerr
		}
		if err := tx.Commit(ctx); err != nil {
			return model.JobRunRecord{}, fmt.Errorf("postgres: commit claim: %w", err)
		}
		return job, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return model.JobRunRecord{}, fmt.Errorf("postgres: commit sweep: %w", err)
	}
	return model.JobRunRecord{}, scheduler.ErrNoQueuedJob
}

type depState int

const (
	depsReady depState = iota
	depsBlocked
	depsFailed
)

func (s *Store) depState(ctx context.Context, q queryer, id model.JobRunID) depState {
	job, err := s.jobIn(ctx, q, id)
	if err != nil || len(job.DependsOn) == 0 {
		return depsReady
	}
	rows, err := q.Query(ctx, `SELECT status FROM job_runs WHERE run_id=$1 AND job_key = ANY($2)`,
		job.RunID, job.DependsOn)
	if err != nil {
		return depsBlocked
	}
	statuses, _ := pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.JobRunStatus, error) {
		var st model.JobRunStatus
		return st, r.Scan(&st)
	})
	if len(statuses) != len(job.DependsOn) {
		return depsBlocked // missing sibling: compile guarantees presence
	}
	state := depsReady
	for _, st := range statuses {
		switch {
		case st == model.JobSucceeded:
		case st.IsTerminal(): // failed, cancelled, skipped
			return depsFailed
		default:
			state = depsBlocked
		}
	}
	return state
}

// ReleaseClaim implements scheduler.DurableStore.
func (s *Store) ReleaseClaim(ctx context.Context, id model.JobRunID) error {
	if _, err := s.pool.Exec(ctx, `UPDATE job_runs SET claimed_at=NULL WHERE id=$1`, id); err != nil {
		return fmt.Errorf("postgres: release claim: %w", err)
	}
	return nil
}

// AckDispatched implements scheduler.DurableStore.
func (s *Store) AckDispatched(ctx context.Context, id model.JobRunID, obj scheduler.ActiveObject, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	job, err := s.jobIn(ctx, tx, id)
	if errors.Is(err, scheduler.ErrJobRunNotFound) {
		return err
	}
	if err != nil {
		return err
	}
	switch job.Status {
	case model.JobDispatched:
		if job.ActiveName != "" && job.ActiveName != obj.Name {
			return fmt.Errorf("postgres: %s already dispatched as %s, refusing %s", id, job.ActiveName, obj.Name)
		}
	case model.JobQueued:
		next, terr := model.TransitionJob(job.Status, model.JobDispatched)
		if terr != nil {
			return fmt.Errorf("%w: %s in status %s", scheduler.ErrJobNotDispatchable, id, job.Status)
		}
		job.Status = next
	default:
		return fmt.Errorf("%w: %s in status %s", scheduler.ErrJobNotDispatchable, id, job.Status)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE job_runs SET status=$2, active_name=$3, active_uid=$4, dispatched_at=$5, claimed_at=NULL
		WHERE id=$1`, id, job.Status, obj.Name, obj.UID, now); err != nil {
		return fmt.Errorf("postgres: ack dispatch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit ack: %w", err)
	}
	return nil
}

// ApplyObserved implements scheduler.DurableStore: monotonic projection.
func (s *Store) ApplyObserved(ctx context.Context, id model.JobRunID, phase model.ObservedPhase, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	job, err := s.jobIn(ctx, tx, id)
	if errors.Is(err, scheduler.ErrJobRunNotFound) {
		return err
	}
	if err != nil {
		return err
	}
	if !job.Status.IsTerminal() {
		if target, changes := model.JobStatusFromPhase(phase); changes && model.CanAdvanceJob(job.Status, target) {
			{
				job.Status = target
				switch target {
				case model.JobRunning:
					if job.StartedAt == nil {
						job.StartedAt = &now
					}
				case model.JobSucceeded, model.JobFailed:
					if job.StartedAt == nil {
						job.StartedAt = &now
					}
					if job.FinishedAt == nil {
						job.FinishedAt = &now
					}
				}
				if _, err := tx.Exec(ctx, `
					UPDATE job_runs SET status=$2, started_at=$3, finished_at=$4 WHERE id=$1`,
					id, job.Status, job.StartedAt, job.FinishedAt); err != nil {
					return fmt.Errorf("postgres: apply observed: %w", err)
				}
			}
		}
	}
	if err := s.refreshRun(ctx, tx, id, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CancelRun implements scheduler.DurableStore.
func (s *Store) CancelRun(ctx context.Context, id model.RunID, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	run, err := s.runIn(ctx, tx, id)
	if errors.Is(err, scheduler.ErrRunNotFound) {
		return err
	}
	if err != nil {
		return err
	}
	if run.Status.IsTerminal() {
		if run.Status == model.RunCancelled {
			return tx.Commit(ctx)
		}
		return fmt.Errorf("postgres: run %s already terminal (%s)", id, run.Status)
	}

	rows, err := tx.Query(ctx, jobCols+` WHERE run_id=$1 FOR UPDATE`, id)
	if err != nil {
		return fmt.Errorf("postgres: load jobs: %w", err)
	}
	jobs, err := pgx.CollectRows(rows, scanJob)
	if err != nil {
		return fmt.Errorf("postgres: load jobs: %w", err)
	}
	for _, job := range jobs {
		if next, terr := model.TransitionJob(job.Status, model.JobCancelled); terr == nil {
			if _, err := tx.Exec(ctx, `UPDATE job_runs SET status=$2, finished_at=$3, claimed_at=NULL WHERE id=$1`,
				job.ID, next, now); err != nil {
				return fmt.Errorf("postgres: cancel job: %w", err)
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE workflow_runs SET status=$2, finished_at=$3 WHERE id=$1`,
		id, model.RunCancelled, now); err != nil {
		return fmt.Errorf("postgres: cancel run: %w", err)
	}
	return tx.Commit(ctx)
}

// ListGCReadyJobs implements scheduler.DurableStore.
func (s *Store) ListGCReadyJobs(ctx context.Context) ([]model.JobRunRecord, error) {
	rows, err := s.pool.Query(ctx, jobCols+`
		WHERE active_collected_at IS NULL
		  AND status IN ('succeeded','failed','cancelled','skipped')
		  AND run_id IN (SELECT id FROM workflow_runs WHERE status IN ('succeeded','failed','cancelled'))
		ORDER BY run_id, id`)
	if err != nil {
		return nil, fmt.Errorf("postgres: gc list: %w", err)
	}
	jobs, err := pgx.CollectRows(rows, scanJob)
	if err != nil {
		return nil, fmt.Errorf("postgres: gc list: %w", err)
	}
	return jobs, nil
}

// MarkCollected implements scheduler.DurableStore.
func (s *Store) MarkCollected(ctx context.Context, id model.JobRunID, now time.Time) error {
	if _, err := s.pool.Exec(ctx, `
		UPDATE job_runs SET active_collected_at=$2 WHERE id=$1 AND active_collected_at IS NULL`, id, now); err != nil {
		return fmt.Errorf("postgres: mark collected: %w", err)
	}
	return nil
}

// refreshRun recomputes the run aggregate monotonically inside the caller's
// transaction.
func (s *Store) refreshRun(ctx context.Context, q queryer, jobID model.JobRunID, now time.Time) error {
	var runID model.RunID
	if err := q.QueryRow(ctx, `SELECT run_id FROM job_runs WHERE id=$1`, jobID).Scan(&runID); err != nil {
		return fmt.Errorf("postgres: job run: %w", err)
	}
	rows, err := q.Query(ctx, `SELECT status FROM job_runs WHERE run_id=$1`, runID)
	if err != nil {
		return fmt.Errorf("postgres: statuses: %w", err)
	}
	statuses, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.JobRunStatus, error) {
		var st model.JobRunStatus
		return st, r.Scan(&st)
	})
	if err != nil {
		return fmt.Errorf("postgres: statuses: %w", err)
	}
	next := model.AggregateRunStatus(statuses)

	var current model.WorkflowRunStatus
	var finished *time.Time
	if err := q.QueryRow(ctx, `SELECT status, finished_at FROM workflow_runs WHERE id=$1 FOR UPDATE`, runID).
		Scan(&current, &finished); err != nil {
		return fmt.Errorf("postgres: lock run: %w", err)
	}
	if !model.CanAdvanceRun(current, next) || next == current {
		return nil
	}
	if next.IsTerminal() && finished == nil {
		finished = &now
	}
	if _, err := q.Exec(ctx, `UPDATE workflow_runs SET status=$2, finished_at=$3 WHERE id=$1`,
		runID, next, finished); err != nil {
		return fmt.Errorf("postgres: refresh run: %w", err)
	}
	return nil
}
