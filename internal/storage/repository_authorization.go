package storage

import (
	"context"
	"database/sql"
	"fmt"
	"unicode/utf8"

	"github.com/shitamachi/forgelet/internal/run"
)

// CancelRepository preserves history and the normal stop/ACK/Pod cleanup path.
// Active attempts retain their slot until their termination is confirmed.
func (s *Store) CancelRepository(ctx context.Context, repository, reason string) (int, error) {
	if repository == "" || len(repository) > 256 || reason == "" || len(reason) > 1024 || !utf8.ValidString(reason) {
		return 0, fmt.Errorf("invalid repository cancellation: %w", ErrLimit)
	}
	changed := 0
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		jobs, err := queryJobs(ctx, tx, `SELECT j.body FROM jobs j JOIN runs r ON r.id=j.run_id WHERE lower(json_extract(r.body,'$.repository'))=lower(?) AND j.state IN ('queued','starting','running') ORDER BY j.created_at,j.id`, repository)
		if err != nil {
			return err
		}
		for _, job := range jobs {
			if job.CancelRequested {
				continue
			}
			job.CancelRequested = true
			job.CancelReason = reason
			if job.State == run.Queued {
				job.State = run.Cancelled
				job.Result = &run.Result{State: run.Cancelled, Error: reason}
			}
			if err := saveTerminalJob(ctx, tx, job); err != nil {
				return err
			}
			changed++
		}
		return nil
	})
	return changed, err
}
