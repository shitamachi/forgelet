package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// GitHubAuthorization is an opaque metadata-only snapshot, separate from the
// operator's enable/disable configuration. Failed discovery must not erase it.
func (s *Store) GitHubAuthorization(ctx context.Context) ([]byte, error) {
	var body []byte
	err := s.db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='github_authorization'").Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return body, err
}
func (s *Store) SaveGitHubAuthorization(ctx context.Context, expected, body []byte) error {
	if len(body) == 0 || len(body) > 4<<20 {
		return ErrLimit
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		var prior []byte
		err := tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='github_authorization'").Scan(&prior)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read GitHub authorization snapshot: %w", err)
		}
		if string(prior) != string(expected) {
			return ErrConflict
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES('github_authorization',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", body); err != nil {
			return fmt.Errorf("save GitHub authorization snapshot: %w", err)
		}
		return nil
	})
}
