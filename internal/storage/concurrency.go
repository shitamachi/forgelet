package storage

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/shitamachi/forgelet/internal/expression"
	"github.com/shitamachi/forgelet/internal/run"
)

func logicalKey(p run.Plan) string {
	if p.Group != "" {
		return p.Group
	}
	return planKey(p)
}
func dependencyGroups(jobs []run.Job) map[string]*run.Job {
	groups := map[string][]run.Job{}
	for _, job := range jobs {
		key := logicalKey(job.Plan)
		groups[key] = append(groups[key], job)
	}
	result := map[string]*run.Job{}
	for key, items := range groups {
		sort.Slice(items, func(i, j int) bool { return items[i].Plan.MatrixIndex < items[j].Plan.MatrixIndex })
		grouped := items[0]
		grouped.State = aggregate(items)
		grouped.Result = &run.Result{State: grouped.State}
		for _, job := range items {
			if job.Result != nil {
				for name, value := range job.Result.Outputs {
					if grouped.Result.Outputs == nil {
						grouped.Result.Outputs = map[string]string{}
					}
					grouped.Result.Outputs[name] = value
				}
			}
			grouped.Plan.AncestorFailed = grouped.Plan.AncestorFailed || job.Plan.AncestorFailed
		}
		result[key] = &grouped
	}
	return result
}
func matrixSlot(job run.Job, jobs []run.Job) bool {
	if job.Plan.MaxParallel == 0 {
		return true
	}
	active := 0
	for _, sibling := range jobs {
		if logicalKey(sibling.Plan) == logicalKey(job.Plan) && (sibling.State == run.Starting || sibling.State == run.Running) {
			active++
		}
	}
	return active < job.Plan.MaxParallel
}
func requestCancellation(ctx context.Context, tx *sql.Tx, job *run.Job) error {
	if job.State.Terminal() || job.CancelRequested {
		return nil
	}
	job.CancelRequested = true
	if job.State == run.Queued {
		job.State = run.Cancelled
		job.Result = &run.Result{State: run.Cancelled}
	}
	return saveJob(ctx, tx, *job)
}

// Propagate at the terminal transition, not only during queue reservation: a
// fully running matrix has no queued job to bring it back through reserveGraph.
func saveTerminalJob(ctx context.Context, tx *sql.Tx, job run.Job) error {
	if err := saveJob(ctx, tx, job); err != nil {
		return err
	}
	if !job.Plan.FailFast || job.Plan.MatrixTotal == 0 || (job.State != run.Failed && job.State != run.TimedOut && job.State != run.Interrupted) {
		return nil
	}
	record, err := getRun(ctx, tx, job.RunID)
	if err != nil {
		return err
	}
	return applyFailFast(ctx, tx, record.Jobs)
}

func applyFailFast(ctx context.Context, tx *sql.Tx, jobs []run.Job) error {
	failed := map[string]bool{}
	for _, job := range jobs {
		if job.Plan.MatrixTotal > 0 && job.Plan.FailFast && (job.State == run.Failed || job.State == run.TimedOut || job.State == run.Interrupted) {
			failed[logicalKey(job.Plan)] = true
		}
	}
	for i := range jobs {
		if failed[logicalKey(jobs[i].Plan)] {
			if err := requestCancellation(ctx, tx, &jobs[i]); err != nil {
				return err
			}
		}
	}
	return nil
}

// Replacement happens when a new run is accepted. Keep at most one pending run
// per repository/group; active work only stops when cancel-in-progress is set.
func applyConcurrencyReplacement(ctx context.Context, tx *sql.Tx, record run.Run) error {
	if record.Concurrency != "" {
		jobs, err := queryJobs(ctx, tx, `SELECT j.body FROM jobs j JOIN runs r ON r.id=j.run_id WHERE r.id<>? AND json_extract(r.body,'$.repository')=? AND lower(json_extract(r.body,'$.concurrency'))=? AND j.state IN ('queued','starting','running')`, record.ID, record.Repository, strings.ToLower(record.Concurrency))
		if err != nil {
			return err
		}
		active := map[string]bool{}
		for _, job := range jobs {
			if _, seen := active[job.RunID]; seen {
				continue
			}
			record, err := getRun(ctx, tx, job.RunID)
			if err != nil {
				return err
			}
			active[job.RunID] = false
			for _, attempt := range record.Jobs {
				active[job.RunID] = active[job.RunID] || attempt.State != run.Queued
			}
		}

		for i := range jobs {
			if record.CancelInProgress || !active[jobs[i].RunID] {
				if err := requestCancellation(ctx, tx, &jobs[i]); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
func concurrencySlot(ctx context.Context, tx *sql.Tx, job run.Job) (bool, error) {
	var blockers int
	if job.Plan.WorkflowConcurrency != "" {
		err := tx.QueryRowContext(ctx, `SELECT count(*) FROM runs r JOIN runs current ON current.id=? WHERE r.id<>current.id AND json_extract(r.body,'$.repository')=? AND lower(json_extract(r.body,'$.concurrency'))=? AND (r.created_at<current.created_at OR (r.created_at=current.created_at AND r.id<current.id)) AND EXISTS(SELECT 1 FROM jobs j WHERE j.run_id=r.id AND j.state IN ('queued','starting','running'))`, job.RunID, job.Plan.Repository, strings.ToLower(job.Plan.WorkflowConcurrency)).Scan(&blockers)
		if err != nil {
			return false, fmt.Errorf("check workflow concurrency: %w", err)
		}
		if blockers > 0 {
			return false, nil
		}
	}
	if job.Plan.Concurrency == "" {
		return true, nil
	}
	var active int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM jobs WHERE id<>? AND state IN ('starting','running') AND json_extract(body,'$.plan.repository')=? AND lower(json_extract(body,'$.plan.concurrency'))=?`, job.ID, job.Plan.Repository, strings.ToLower(job.Plan.Concurrency)).Scan(&active)
	return active == 0, err
}

func reconcileJobConcurrency(ctx context.Context, tx *sql.Tx) error {
	jobs, err := queryJobs(ctx, tx, `SELECT body FROM jobs WHERE state IN ('queued','starting','running') AND COALESCE(json_extract(body,'$.plan.concurrency'),'')<>'' ORDER BY created_at,id`)
	if err != nil {
		return err
	}
	groups := map[string][]run.Job{}
	graphs := map[string]map[string]*run.Job{}
	for _, job := range jobs {
		if job.CancelRequested {
			continue
		}
		if job.State == run.Queued {
			graph := graphs[job.RunID]
			if graph == nil {
				record, err := getRun(ctx, tx, job.RunID)
				if err != nil {
					return err
				}
				graph = dependencyGroups(record.Jobs)
				graphs[job.RunID] = graph
			}
			if !dependenciesTerminal(job.Plan, graph) {
				continue
			}
			allowed, err := expression.Evaluate(job.Plan.If, dependencyContext(&job, graph))
			if err != nil || !allowed {
				continue
			}
		}
		key := job.Plan.Repository + "\x00" + strings.ToLower(job.Plan.Concurrency)
		groups[key] = append(groups[key], job)
	}
	for _, items := range groups {
		latest := -1
		for i := range items {
			if items[i].State == run.Queued {
				latest = i
			}
		}
		if latest < 0 {
			continue
		}
		for i := range items {
			if i == latest {
				continue
			}
			if items[i].State == run.Queued || items[latest].Plan.CancelInProgress {
				if err := requestCancellation(ctx, tx, &items[i]); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
