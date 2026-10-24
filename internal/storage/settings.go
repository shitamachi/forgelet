package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// Setting values are opaque: secrets arrive already encrypted by the server.
func (s *Store) PutSetting(ctx context.Context, repository, kind, name string, value []byte) error {
	if (kind != "secrets" && kind != "vars") || repository == "" || name == "" || len(value) > 32<<10 {
		return fmt.Errorf("invalid repository setting: %w", ErrLimit)
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM settings WHERE repository=? AND kind=? AND name<>?", repository, kind, name).Scan(&count); err != nil {
			return err
		}
		if count >= 100 {
			return fmt.Errorf("at most 100 settings of each kind: %w", ErrLimit)
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO settings(repository,kind,name,value) VALUES(?,?,?,?) ON CONFLICT(repository,kind,name) DO UPDATE SET value=excluded.value", repository, kind, name, value)
		return err
	})
}
func (s *Store) Settings(ctx context.Context, repository, kind string) (map[string][]byte, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT name,value FROM settings WHERE repository=? AND kind=? ORDER BY name", repository, kind)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := map[string][]byte{}
	for rows.Next() {
		var name string
		var value []byte
		if err := rows.Scan(&name, &value); err != nil {
			return nil, err
		}
		result[name] = value
	}
	return result, rows.Err()
}
func (s *Store) DeleteSetting(ctx context.Context, repository, kind, name string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM settings WHERE repository=? AND kind=? AND name=?", repository, kind, name)
	return err
}
