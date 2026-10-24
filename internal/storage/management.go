package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// The single durable document uses an existing metadata row and participates in backups.
func (s *Store) Management(ctx context.Context) ([]byte, error) {
	var body []byte
	err := s.db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='management'").Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return body, err
}
func (s *Store) SaveManagement(ctx context.Context, expected, body []byte) error {
	if len(body) > 4<<20 {
		return ErrLimit
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		var old []byte
		err := tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='management'").Scan(&old)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if string(old) != string(expected) {
			return ErrConflict
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES('management',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", body)
		if err != nil {
			return fmt.Errorf("save management: %w", err)
		}
		return nil
	})
}
