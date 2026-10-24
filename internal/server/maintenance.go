package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/shitamachi/forgelet/internal/storage"
)

// Retention is an age policy. Active work is protected independently of its age.
type Retention struct {
	History  time.Duration
	Logs     time.Duration
	Events   time.Duration
	Backups  time.Duration
	Interval time.Duration
}

func DefaultRetention() Retention {
	return Retention{History: 30 * 24 * time.Hour, Logs: 7 * 24 * time.Hour, Events: 30 * 24 * time.Hour, Backups: 7 * 24 * time.Hour, Interval: 10 * time.Minute}
}

func (r Retention) Validate() error {
	for name, age := range map[string]time.Duration{"history": r.History, "logs": r.Logs, "events": r.Events, "backups": r.Backups, "interval": r.Interval} {
		if age <= 0 || age > 3650*24*time.Hour {
			return fmt.Errorf("retention.%s must be positive and at most 3650 days", name)
		}
	}
	if r.Logs > r.History {
		return errors.New("retention.logs must not exceed retention.history")
	}
	return nil
}

type MaintenanceResult struct {
	storage.PruneResult
	Backups int `json:"backups"`
	Blobs   int `json:"blobs"`
}

func (s *Server) maintenance(w http.ResponseWriter, r *http.Request) {
	s.tickMu.Lock()
	defer s.tickMu.Unlock()
	result, err := s.maintain(r.Context(), s.now())
	respond(w, result, err)
}

// maintain runs under tickMu so Pod GC and pruning cannot race within this server.
func (s *Server) maintain(ctx context.Context, now time.Time) (MaintenanceResult, error) {
	var result MaintenanceResult
	if s.cfg.Blobs != nil {
		var err error
		result.Blobs, err = s.cfg.Blobs.Prune(ctx, now)
		if err != nil {
			return result, err
		}
	}
	pods, err := s.runtime.Pods(ctx)
	if err != nil {
		return result, fmt.Errorf("observe pods before retention: %w", err)
	}
	r := s.cfg.Retention
	result.PruneResult, err = s.store.Prune(ctx, now, storage.RetentionPolicy{History: r.History, Logs: r.Logs, Events: r.Events, BatchSize: 100}, pods)
	if err != nil {
		return result, fmt.Errorf("prune history: %w", err)
	}
	result.Backups, err = pruneBackups(ctx, s.cfg.DataDir, now.Add(-r.Backups), 100)
	if err != nil {
		return result, err
	}
	s.nextMaintenance = now.Add(r.Interval)
	return result, nil
}

var backupName = regexp.MustCompile(`^backup-[a-f0-9]{32}\.db$`)

func pruneBackups(ctx context.Context, directory string, before time.Time, limit int) (int, error) {
	if directory == "" {
		return 0, nil
	}
	dir, err := os.Open(directory)
	if err != nil {
		return 0, fmt.Errorf("open backup directory: %w", err)
	}
	defer func() { _ = dir.Close() }()
	removed := 0
	for removed < limit {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		entries, readErr := dir.ReadDir(100)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return removed, fmt.Errorf("read backup directory: %w", readErr)
		}
		for _, entry := range entries {
			if !backupName.MatchString(entry.Name()) || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			info, err := entry.Info()
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return removed, fmt.Errorf("inspect backup: %w", err)
			}
			if !info.Mode().IsRegular() || !info.ModTime().Before(before) {
				continue
			}
			if err := os.Remove(filepath.Join(directory, entry.Name())); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return removed, fmt.Errorf("remove expired backup: %w", err)
			}
			removed++
			if removed == limit {
				return removed, nil
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	return removed, nil
}
