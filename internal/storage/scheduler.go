package storage

import (
	"context"
	"database/sql"
	"encoding/json"

	"fmt"

	"github.com/shitamachi/forgelet/internal/expression"
	"github.com/shitamachi/forgelet/internal/run"
)

func planKey(plan run.Plan) string {
	if plan.Key != "" {
		return plan.Key
	}
	return plan.JobID // Plans persisted before DAG support have no workflow key.
}

func validateGraph(runID string, plans []run.Plan) error {
	if len(plans) > 256 {
		return fmt.Errorf("at most 256 expanded jobs per run: %w", ErrLimit)
	}
	byKey := make(map[string]run.Plan, len(plans))
	for _, plan := range plans {
		key := logicalKey(plan)
		if plan.JobID == "" || plan.RunID != runID {
			return fmt.Errorf("plan identity mismatch: %w", ErrConflict)
		}
		if _, exists := byKey[key]; exists && plan.MatrixTotal == 0 {
			return fmt.Errorf("duplicate job key %q: %w", key, ErrConflict)
		}
		byKey[key] = plan
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(key string) error {
		plan, exists := byKey[key]
		if !exists {
			return fmt.Errorf("unknown dependency %q: %w", key, ErrConflict)
		}
		if visiting[key] {
			return fmt.Errorf("cyclic dependency %q: %w", key, ErrConflict)
		}
		if visited[key] {
			return nil
		}
		visiting[key] = true
		for _, need := range plan.Needs {
			if err := visit(need); err != nil {
				return err
			}
		}
		visiting[key], visited[key] = false, true
		return nil
	}
	for key := range byKey {
		if err := visit(key); err != nil {
			return err
		}
	}
	return nil
}

// Reserve with zero capacity still reconciles conditions, failure propagation
// and concurrency cancellation, without moving any queued job to starting.
func (s *Store) Reserve(ctx context.Context, maxParallel int) ([]run.Job, error) {
	if maxParallel < 0 {
		return nil, fmt.Errorf("parallelism must be positive: %w", ErrLimit)
	}
	var reserved []run.Job
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if err := reconcileJobConcurrency(ctx, tx); err != nil {
			return err
		}
		var active int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM jobs WHERE state IN (?,?)", run.Starting, run.Running).Scan(&active); err != nil {
			return fmt.Errorf("count active jobs: %w", err)
		}
		// Page run identities, loading at most one bounded job graph at a time.
		// Waiting jobs never consume the quota or prevent examining later runs.
		var afterTime int64
		var afterID string
		for {
			page, err := queuedRunPage(ctx, tx, afterTime, afterID)
			if err != nil {
				return err
			}
			if len(page) == 0 {
				return nil
			}
			for _, row := range page {
				jobs, err := queryJobs(ctx, tx, "SELECT body FROM jobs WHERE run_id=? ORDER BY created_at,id", row.id)
				if err != nil {
					return err
				}
				next, err := reserveGraph(ctx, tx, jobs, maxParallel-active)
				if err != nil {
					return err
				}
				reserved = append(reserved, next...)
				active += len(next)
				afterTime, afterID = row.created, row.id
			}
		}
	})
	if err != nil {
		return nil, err
	}
	return reserved, nil
}

type queuedRun struct {
	id      string
	created int64
}

func queuedRunPage(ctx context.Context, tx *sql.Tx, afterTime int64, afterID string) ([]queuedRun, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,created_at FROM runs
 WHERE (created_at>? OR (created_at=? AND id>?))
 AND EXISTS(SELECT 1 FROM jobs WHERE jobs.run_id=runs.id AND state='queued')
 ORDER BY created_at,id LIMIT 64`, afterTime, afterTime, afterID)
	if err != nil {
		return nil, fmt.Errorf("query queued runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var page []queuedRun
	for rows.Next() {
		var row queuedRun
		if err := rows.Scan(&row.id, &row.created); err != nil {
			return nil, fmt.Errorf("scan queued run: %w", err)
		}
		page = append(page, row)
	}
	return page, rows.Err()
}

func reserveGraph(ctx context.Context, tx *sql.Tx, jobs []run.Job, slots int) ([]run.Job, error) {
	if err := applyFailFast(ctx, tx, jobs); err != nil {
		return nil, err
	}
	byKey := dependencyGroups(jobs)
	var reserved []run.Job
	// Skipping one job can make its children decidable in this same transaction.
	for changed := true; changed; {
		changed = false
		for i := range jobs {
			job := &jobs[i]
			if job.State != run.Queued || job.CancelRequested || !dependenciesTerminal(job.Plan, byKey) {
				continue
			}
			eval := dependencyContext(job, byKey)
			allowed, err := expression.Evaluate(job.Plan.If, eval)
			if err != nil {
				// Compilation normally rejects invalid conditions. A corrupt legacy
				// plan must fail visibly instead of blocking every other run.
				job.State, job.Result = run.Failed, &run.Result{State: run.Failed, Error: "job condition: " + err.Error()}
			} else if !allowed {
				job.State, job.Result = run.Skipped, &run.Result{State: run.Skipped}
			} else if planErr := claimPlanLimit(job.Plan); planErr != nil {
				job.Plan.NeedsContext = nil
				job.State, job.Result = run.Failed, &run.Result{State: run.Failed, Error: planErr.Error()}
			} else if slots > 0 {
				if !matrixSlot(*job, jobs) {
					continue
				}
				available, err := concurrencySlot(ctx, tx, *job)
				if err != nil {
					return nil, err
				}
				if !available {
					continue
				}
				job.State = run.Starting
				slots--
			} else {
				continue
			}
			if err := saveJob(ctx, tx, *job); err != nil {
				return nil, err
			}
			if job.State == run.Starting {
				reserved = append(reserved, *job)
			}
			byKey = dependencyGroups(jobs)
			changed = true
		}
	}
	return reserved, nil
}

// Dependencies are resolved after run admission and can enlarge a persisted
// plan. Reject oversized snapshots before creating an unclaimable executor.
func claimPlanLimit(plan run.Plan) error {
	data, err := json.Marshal(plan)
	if err != nil {
		return fmt.Errorf("encode resolved execution plan: %w", err)
	}
	if len(data) > run.MaxPlanBytes {
		return fmt.Errorf("resolved execution plan is %d bytes; limit is %d", len(data), run.MaxPlanBytes)
	}
	return nil
}

func dependenciesTerminal(plan run.Plan, jobs map[string]*run.Job) bool {
	for _, key := range plan.Needs {
		job, exists := jobs[key]
		if !exists || !job.State.Terminal() {
			return false
		}
	}
	return true
}

func dependencyContext(job *run.Job, jobs map[string]*run.Job) expression.Context {
	values, typed := job.Plan.Contexts()
	for name, value := range job.Plan.Env {
		values["env."+name] = value
	}
	eval := expression.Context{Values: values, Typed: typed, Success: true}
	job.Plan.NeedsContext = nil
	if len(job.Plan.Needs) > 0 {
		job.Plan.NeedsContext = make(map[string]run.Dependency, len(job.Plan.Needs))
	}
	for _, key := range job.Plan.Needs {
		dependency := jobs[key]
		state := dependency.State
		if state == run.TimedOut || state == run.Interrupted {
			state = run.Failed
		}
		entry := run.Dependency{Result: state}
		if dependency.Result != nil {
			entry.Outputs = dependency.Result.Outputs
		}
		job.Plan.NeedsContext[key] = entry
		values["needs."+key+".result"] = string(state)
		for name, value := range entry.Outputs {
			values["needs."+key+".outputs."+name] = value
		}
		eval.Success = eval.Success && state == run.Succeeded
	}
	seen := map[string]bool{}
	var ancestors func([]string)
	ancestors = func(keys []string) {
		for _, key := range keys {
			if seen[key] {
				continue
			}
			seen[key] = true
			dependency := jobs[key]
			eval.Failure = eval.Failure || dependency.State == run.Failed || dependency.State == run.TimedOut || dependency.State == run.Interrupted || dependency.Plan.AncestorFailed
			eval.Cancelled = eval.Cancelled || dependency.State == run.Cancelled
			ancestors(dependency.Plan.Needs)
		}
	}
	ancestors(job.Plan.Needs)
	job.Plan.AncestorFailed = eval.Failure
	return eval
}
