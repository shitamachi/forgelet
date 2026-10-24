package storage

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestAuthPersistenceRevocationAndBoundedSessions(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	now := time.Now()
	if err := s.AuthInitialize(ctx, []byte("hash-one")); err != nil {
		t.Fatal(err)
	}
	if err := s.AuthInitialize(ctx, []byte("must-not-reset")); err != nil {
		t.Fatal(err)
	}
	u, err := s.AuthAdmin(ctx)
	if err != nil || string(u.PasswordHash) != "hash-one" {
		t.Fatal(u, err)
	}
	for i := 0; i < 40; i++ {
		if err = s.AuthNewSession(ctx, string(rune('a'+i)), now.Add(time.Duration(i)*time.Nanosecond), now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM auth_sessions").Scan(&count); err != nil || count != 32 {
		t.Fatal(count, err)
	}
	if _, err = s.AuthSession(ctx, "a", now); !errors.Is(err, ErrNotFound) {
		t.Fatal("oldest session retained", err)
	}
	if _, err = s.AuthSession(ctx, string(rune('a'+39)), now.Add(2*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired session accepted", err)
	}
	if err = s.AuthGitHub(ctx, 123, "owner", []byte("ciphertext")); err != nil {
		t.Fatal(err)
	}
	if err = s.AuthGitHub(ctx, 999, "attacker", []byte("evil")); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err = s.AuthNewFlow(ctx, "flow", []byte("sealed"), now, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	body, err := s.AuthConsumeFlow(ctx, "flow", now)
	if err != nil || string(body) != "sealed" {
		t.Fatal(string(body), err)
	}
	if _, err = s.AuthConsumeFlow(ctx, "flow", now); !errors.Is(err, ErrNotFound) {
		t.Fatal("state replay accepted", err)
	}
	if err = s.AuthPassword(ctx, []byte("wrong"), []byte("new")); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err = s.AuthPassword(ctx, []byte("hash-one"), []byte("new")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AuthSession(ctx, string(rune('a'+39)), now); !errors.Is(err, ErrNotFound) {
		t.Fatal("password change did not revoke sessions", err)
	}
	path := s.lock.Name()
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(ctx, filepath.Join(filepath.Dir(path), "forgelet.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	u, err = s2.AuthAdmin(ctx)
	if err != nil || u.GitHubID != 123 || string(u.GitHubToken) != "ciphertext" || string(u.PasswordHash) != "new" {
		t.Fatal(u, err)
	}
}
func TestPrepareRestoreRevokesAuthSessionsAndFlows(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	now := time.Now()
	for _, err := range []error{s.AuthInitialize(ctx, []byte("hash")), s.AuthNewSession(ctx, "session", now, now.Add(time.Hour)), s.AuthNewFlow(ctx, "flow", []byte("secret"), now, now.Add(time.Hour))} {
		if err != nil {
			t.Fatal(err)
		}
	}
	backup := filepath.Join(t.TempDir(), "backup.db")
	if err := s.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	if err := PrepareRestore(ctx, backup); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(ctx, backup)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	if _, err = restored.AuthSession(ctx, "session", now); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err = restored.AuthConsumeFlow(ctx, "flow", now); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err = restored.AuthAdmin(ctx); err != nil {
		t.Fatal("administrator identity lost on restore", err)
	}
}
