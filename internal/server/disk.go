package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/shitamachi/forgelet/internal/storage"
)

type DiskSpace struct {
	PauseBelowBytes uint64 `yaml:"pause_below_bytes" json:"pause_below_bytes"`
	ResumeAtBytes   uint64 `yaml:"resume_at_bytes" json:"resume_at_bytes"`
}

func DefaultDiskSpace() DiskSpace {
	return DiskSpace{PauseBelowBytes: 512 << 20, ResumeAtBytes: 1 << 30}
}

func (d DiskSpace) Validate() error {
	if d.PauseBelowBytes == 0 || d.ResumeAtBytes <= d.PauseBelowBytes {
		return errors.New("disk_space requires 0 < pause_below_bytes < resume_at_bytes")
	}
	return nil
}

type DiskStatus struct {
	DiskSpace
	Paused         bool      `json:"paused"`
	Reason         string    `json:"reason,omitempty"`
	AvailableBytes *uint64   `json:"available_bytes"`
	CheckedAt      time.Time `json:"checked_at"`
	Error          string    `json:"error,omitempty"`
}

type diskGuard struct {
	mu     sync.RWMutex
	dir    string
	probe  func(context.Context, string) (uint64, error)
	status DiskStatus
}

func newDiskGuard(dir string, policy DiskSpace) *diskGuard {
	// On restart require the recovery threshold again. Recomputing conservatively
	// avoids a database write on a pressured disk and cannot bypass a prior pause.
	return &diskGuard{dir: dir, probe: storage.AvailableBytes, status: DiskStatus{
		DiskSpace: policy, Paused: true, Reason: "disk_not_checked",
	}}
}

// check is called under tickMu; the separate lock keeps status reads independent
// of provider/Kubernetes requests in a reconciliation tick.
func (d *diskGuard) check(ctx context.Context, now time.Time) error {
	available, err := d.probe(ctx, d.dir)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.status.CheckedAt = now.UTC()
	d.status.Error = ""
	if err != nil {
		d.status.Paused = true
		d.status.Reason = "disk_check_failed"
		d.status.AvailableBytes = nil
		d.status.Error = err.Error()
		return fmt.Errorf("check scheduling disk space: %w", err)
	}
	d.status.AvailableBytes = &available
	if available < d.status.PauseBelowBytes {
		d.status.Paused = true
	} else if available >= d.status.ResumeAtBytes {
		d.status.Paused = false
	}
	d.status.Reason = ""
	if d.status.Paused {
		d.status.Reason = "disk_low"
	}
	return nil
}

func (d *diskGuard) snapshot() DiskStatus {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.status
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]DiskStatus{"disk": s.disk.snapshot()})
}
