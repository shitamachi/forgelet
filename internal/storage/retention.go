package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/shitamachi/forgelet/internal/run"
)

type RetentionPolicy struct {
	History   time.Duration
	Logs      time.Duration
	Events    time.Duration
	BatchSize int
}

type PruneResult struct {
	Deployments int `json:"deployments"`
	Runs        int `json:"runs"`
	Jobs        int `json:"jobs"`
	Logs        int `json:"logs"`
	Events      int `json:"events"`
}

// Prune uses a successfully observed job ID -> Pod UID map. A job remains
// protected even when an observed UID differs; reconciliation must resolve it.
// Each category processes at most BatchSize runs, jobs, or event payloads.
// Logs counts removed chunks. Delivery identities are intentionally retained.
func (s *Store) Prune(ctx context.Context, now time.Time, policy RetentionPolicy, activePods map[string]string) (PruneResult, error) {
	var result PruneResult
	if policy.History <= 0 || policy.Logs <= 0 || policy.Events <= 0 || policy.BatchSize < 1 || policy.BatchSize > 1000 || now.IsZero() {
		return result, fmt.Errorf("retention durations must be positive and batch size 1–1000: %w", ErrLimit)
	}
	pods, err := json.Marshal(activePods)
	if err != nil {
		return result, fmt.Errorf("encode observed Pods: %w", err)
	}
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		deleted, err := tx.ExecContext(ctx, `DELETE FROM deliveries WHERE id IN (SELECT d.id FROM deliveries d WHERE d.state IN ('superseded','cancelled','conflict') AND d.updated_at<=? AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.run_id=d.run_id AND j.terminal_at=0) ORDER BY d.updated_at LIMIT ?)`, now.Add(-policy.History).UnixNano(), policy.BatchSize)
		if err != nil {
			return fmt.Errorf("prune deliveries: %w", err)
		}
		if err := addCount(deleted, &result.Deployments); err != nil {
			return err
		}
		if err := expireLogs(ctx, tx, now.Add(-policy.Logs).UnixNano(), policy.BatchSize, string(pods), &result); err != nil {
			return err
		}
		if err := expireHistory(ctx, tx, now.Add(-policy.History).UnixNano(), policy.BatchSize, string(pods), &result); err != nil {
			return err
		}
		return expireEvents(ctx, tx, now.Add(-policy.Events).UnixNano(), policy.BatchSize, string(pods), &result)
	})
	if err != nil {
		return PruneResult{}, err
	}
	if result.Runs+result.Logs+result.Events+result.Deployments > 0 {
		// This is bounded and does nothing for v1 databases in auto_vacuum=NONE.
		// Such databases reuse deleted pages without rewriting the live database.
		if _, err := s.db.ExecContext(ctx, "PRAGMA incremental_vacuum(128)"); err != nil {
			return result, fmt.Errorf("reclaim free pages: %w", err)
		}
	}
	return result, nil
}

func expireLogs(ctx context.Context, tx *sql.Tx, before int64, batch int, pods string, result *PruneResult) error {
	jobs, err := queryJobs(ctx, tx, `SELECT j.body FROM jobs j
 WHERE j.terminal_at>0 AND j.terminal_at<=? AND j.logs_expired=0
 AND NOT EXISTS(SELECT 1 FROM jobs sibling WHERE sibling.run_id=j.run_id AND sibling.terminal_at=0)
 AND NOT EXISTS(SELECT 1 FROM jobs sibling JOIN json_each(?) pod ON pod.key=sibling.id WHERE sibling.run_id=j.run_id)
 ORDER BY j.terminal_at,j.id LIMIT ?`, before, pods, batch)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		deleted, err := tx.ExecContext(ctx, "DELETE FROM logs WHERE job_id=?", job.ID)
		if err != nil {
			return fmt.Errorf("expire job logs: %w", err)
		}
		if err := addCount(deleted, &result.Logs); err != nil {
			return err
		}
		job.LogsExpired = true
		body, err := json.Marshal(job)
		if err != nil {
			return fmt.Errorf("encode expired logs: %w", err)
		}
		// Preserve the execution's UpdatedAt and immutable terminal time.
		if _, err := tx.ExecContext(ctx, "UPDATE jobs SET body=?,logs_expired=1,log_bytes=0 WHERE id=?", body, job.ID); err != nil {
			return fmt.Errorf("mark logs expired: %w", err)
		}
	}
	return nil
}

func expireHistory(ctx context.Context, tx *sql.Tx, before int64, batch int, pods string, result *PruneResult) error {
	rows, err := tx.QueryContext(ctx, `SELECT r.id FROM runs r
 WHERE NOT EXISTS(SELECT 1 FROM deliveries d WHERE d.run_id=r.id AND d.state='queued')
 AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.run_id=r.id AND
   (j.terminal_at=0 OR j.terminal_at>? OR (r.installation_id>0 AND j.check_dirty=1)))
 AND NOT EXISTS(SELECT 1 FROM jobs j JOIN json_each(?) pod ON pod.key=j.id WHERE j.run_id=r.id)
 ORDER BY r.created_at,r.id LIMIT ?`, before, pods, batch)
	if err != nil {
		return fmt.Errorf("select expired history: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan expired run: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("read expired runs: %w", err)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, "INSERT INTO run_tombstones(id,delivery_id,workflow_path) SELECT id,delivery_id,workflow_path FROM runs WHERE id=?", id); err != nil {
			return fmt.Errorf("retain expired run identity: %w", err)
		}
		for _, deletion := range []struct {
			query string
			count *int
		}{
			{"DELETE FROM logs WHERE job_id IN (SELECT id FROM jobs WHERE run_id=?)", &result.Logs},
			{"DELETE FROM jobs WHERE run_id=?", &result.Jobs},
			{"DELETE FROM runs WHERE id=?", &result.Runs},
		} {
			deleted, err := tx.ExecContext(ctx, deletion.query, id)
			if err != nil {
				return fmt.Errorf("delete expired history: %w", err)
			}
			if err := addCount(deleted, deletion.count); err != nil {
				return err
			}
		}
	}
	return nil
}

func expireEvents(ctx context.Context, tx *sql.Tx, before int64, batch int, pods string, result *PruneResult) error {
	rows, err := tx.QueryContext(ctx, `SELECT e.body FROM events e
 WHERE e.done=1 AND e.payload_expired=0 AND e.created_at<=?
 AND NOT EXISTS(SELECT 1 FROM runs r JOIN jobs j ON j.run_id=r.id
   WHERE r.delivery_id=e.delivery_id AND (j.terminal_at=0 OR j.id IN(SELECT key FROM json_each(?))))
 ORDER BY e.created_at,e.delivery_id LIMIT ?`, before, pods, batch)
	if err != nil {
		return fmt.Errorf("select expired event payloads: %w", err)
	}
	var events []run.Event
	for rows.Next() {
		var body []byte
		var event run.Event
		if err := rows.Scan(&body); err != nil {
			_ = rows.Close()
			return fmt.Errorf("read expired event: %w", err)
		}
		if err := json.Unmarshal(body, &event); err != nil {
			_ = rows.Close()
			return fmt.Errorf("decode expired event: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("read expired events: %w", err)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, event := range events {
		event.Payload = nil
		body, err := json.Marshal(event)
		if err != nil {
			return fmt.Errorf("encode expired event: %w", err)
		}
		deleted, err := tx.ExecContext(ctx, "UPDATE events SET body=?,payload_expired=1 WHERE delivery_id=?", body, event.DeliveryID)
		if err != nil {
			return fmt.Errorf("expire event payload: %w", err)
		}
		if err := addCount(deleted, &result.Events); err != nil {
			return err
		}
	}
	return nil
}

func addCount(result sql.Result, count *int) error {
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count expired records: %w", err)
	}
	*count += int(n)
	return nil
}
