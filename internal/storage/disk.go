package storage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"syscall"
)

// AvailableBytes measures the filesystem containing dir, including SQLite,
// blobs and backups. Bavail excludes blocks reserved for privileged processes.
// A PVC request (notably local-path) is not necessarily a filesystem quota.
func AvailableBytes(ctx context.Context, dir string) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return 0, fmt.Errorf("stat data filesystem: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if stat.Bsize <= 0 || uint64(stat.Bavail) > math.MaxUint64/uint64(stat.Bsize) {
		return 0, errors.New("invalid data filesystem capacity")
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), nil
}
