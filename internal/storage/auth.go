package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// AuthUser is the single homelab administrator. Credentials never serialize.
type AuthUser struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	GitHubID     int64  `json:"github_id,omitempty"`
	GitHubLogin  string `json:"github_login,omitempty"`
	PasswordHash []byte `json:"-"`
	GitHubToken  []byte `json:"-"`
}

func (s *Store) migrateV5(ctx context.Context) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE TABLE auth_admin(id INTEGER PRIMARY KEY CHECK(id=1),username TEXT NOT NULL UNIQUE,password_hash BLOB NOT NULL,github_id INTEGER NOT NULL DEFAULT 0,github_login TEXT NOT NULL DEFAULT '',github_token BLOB);
CREATE TABLE auth_sessions(hash TEXT PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES auth_admin(id),expires_at INTEGER NOT NULL,created_at INTEGER NOT NULL);
CREATE TABLE auth_flows(hash TEXT PRIMARY KEY,body BLOB NOT NULL,expires_at INTEGER NOT NULL);
PRAGMA user_version=5;`)
		if err != nil {
			return fmt.Errorf("migrate authentication: %w", err)
		}
		return nil
	})
}
func (s *Store) AuthAdmin(ctx context.Context) (AuthUser, error) {
	var u AuthUser
	err := s.db.QueryRowContext(ctx, "SELECT id,username,password_hash,github_id,github_login,github_token FROM auth_admin WHERE id=1").Scan(&u.ID, &u.Username, &u.PasswordHash, &u.GitHubID, &u.GitHubLogin, &u.GitHubToken)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}
func (s *Store) AuthInitialize(ctx context.Context, passwordHash []byte) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO auth_admin(id,username,password_hash) VALUES(1,'admin',?) ON CONFLICT(id) DO NOTHING", passwordHash)
	return err
}
func (s *Store) AuthPassword(ctx context.Context, oldHash, newHash []byte) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE auth_admin SET password_hash=? WHERE id=1 AND password_hash=?", newHash, oldHash)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrConflict
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM auth_sessions"); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM auth_flows")
		return err
	})
}
func (s *Store) AuthGitHub(ctx context.Context, id int64, login string, token []byte) error {
	result, err := s.db.ExecContext(ctx, "UPDATE auth_admin SET github_id=?,github_login=?,github_token=? WHERE id=1 AND (github_id=0 OR github_id=?)", id, login, token, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}
func (s *Store) AuthSession(ctx context.Context, hash string, now time.Time) (AuthUser, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, "SELECT user_id FROM auth_sessions WHERE hash=? AND expires_at>?", hash, now.Unix()).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthUser{}, ErrNotFound
	}
	if err != nil {
		return AuthUser{}, err
	}
	return s.AuthAdmin(ctx)
}
func (s *Store) AuthNewSession(ctx context.Context, hash string, now, expires time.Time) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM auth_sessions WHERE expires_at<=?", now.Unix()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM auth_sessions WHERE hash IN (SELECT hash FROM auth_sessions ORDER BY created_at DESC,hash LIMIT -1 OFFSET 31)"); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO auth_sessions(hash,user_id,expires_at,created_at) VALUES(?,1,?,?)", hash, expires.Unix(), now.UnixNano())
		return err
	})
}
func (s *Store) AuthLogout(ctx context.Context, hash string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM auth_sessions WHERE hash=?", hash)
	return err
}
func (s *Store) AuthNewFlow(ctx context.Context, hash string, body []byte, now, expires time.Time) error {
	if len(body) > 8192 {
		return ErrLimit
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM auth_flows WHERE expires_at<=?", now.Unix()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM auth_flows WHERE hash IN (SELECT hash FROM auth_flows ORDER BY expires_at DESC,hash LIMIT -1 OFFSET 15)"); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO auth_flows(hash,body,expires_at) VALUES(?,?,?)", hash, body, expires.Unix())
		return err
	})
}

// AuthConsumeFlow atomically burns state before any network exchange, including errors.
func (s *Store) AuthConsumeFlow(ctx context.Context, hash string, now time.Time) ([]byte, error) {
	var body []byte
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, "DELETE FROM auth_flows WHERE hash=? AND expires_at>? RETURNING body", hash, now.Unix()).Scan(&body)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return body, err
}
