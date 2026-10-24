package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAvailableBytes(t *testing.T) {
	dir := t.TempDir()
	if available, err := AvailableBytes(t.Context(), dir); err != nil || available == 0 {
		t.Fatalf("actual filesystem: %d %v", available, err)
	}
	if _, err := AvailableBytes(t.Context(), filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing data directory: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := AvailableBytes(ctx, dir); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled probe: %v", err)
	}
}
