package storage

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
)

func TestAuthorizationSnapshotCASAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	first := []byte(`{"version":1,"pending":true}`)
	if err := s.SaveGitHubAuthorization(t.Context(), nil, first); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveGitHubAuthorization(t.Context(), nil, []byte(`{"version":2}`)); !errors.Is(err, ErrConflict) {
		t.Fatal("stale snapshot overwrote current state", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GitHubAuthorization(t.Context())
	if err != nil || !bytes.Equal(got, first) {
		t.Fatalf("snapshot lost: %s %v", got, err)
	}
	if err := s.SaveGitHubAuthorization(t.Context(), got, []byte(`{"version":2,"pending":false}`)); err != nil {
		t.Fatal(err)
	}
}
