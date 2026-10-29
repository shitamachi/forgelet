package storage

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/shitamachi/forgelet/internal/run"
)

// PinEventSHA commits the resolved merge before any workflow is admitted. A
// retry/restart always reuses the first pinned SHA, even if GitHub later changes.
func (s *Store) PinEventSHA(ctx context.Context, id, sha string) (run.Event, error) {
	var event run.Event
	if _, err := hex.DecodeString(sha); err != nil || len(sha) != 40 {
		return event, fmt.Errorf("invalid event SHA: %w", ErrConflict)
	}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var body []byte
		var done int
		if err := tx.QueryRowContext(ctx, "SELECT body,done FROM events WHERE delivery_id=?", id).Scan(&body, &done); err != nil {
			return notFound(err)
		}
		if err := json.Unmarshal(body, &event); err != nil {
			return fmt.Errorf("decode unresolved event: %w", err)
		}
		if done != 0 || event.Name != "pull_request" {
			return ErrConflict
		}
		if event.SHA != "" {
			return nil
		}
		event.SHA = sha
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return fmt.Errorf("decode PR payload: %w", err)
		}
		var pr map[string]json.RawMessage
		if err := json.Unmarshal(payload["pull_request"], &pr); err != nil || pr == nil {
			return fmt.Errorf("missing PR payload: %w", ErrConflict)
		}
		pr["merge_commit_sha"], _ = json.Marshal(sha)
		payload["pull_request"], _ = json.Marshal(pr)
		event.Payload, _ = json.Marshal(payload)
		body, err := json.Marshal(event)
		if err != nil {
			return fmt.Errorf("encode resolved event: %w", err)
		}
		_, err = tx.ExecContext(ctx, "UPDATE events SET body=? WHERE delivery_id=?", body, id)
		return err
	})
	return event, err
}
