package storage

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestManagementCASAndReopen(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Management(t.Context())
	if err != nil || len(first) != 0 {
		t.Fatal(err)
	}
	if err = s.SaveManagement(t.Context(), nil, []byte(`{"version":1}`)); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveManagement(t.Context(), nil, []byte(`{"version":2}`)); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	got, err := s.Management(t.Context())
	if err != nil || string(got) != `{"version":1}` {
		t.Fatal(string(got), err)
	}
	if err = s.SaveManagement(t.Context(), got, []byte(`{"version":2}`)); err != nil {
		t.Fatal(err)
	}
}
