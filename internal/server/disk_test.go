package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run"
	"github.com/shitamachi/forgelet/internal/storage"
)

func TestDiskGuardHysteresisAndProbeFailure(t *testing.T) {
	d := newDiskGuard("/data", DiskSpace{PauseBelowBytes: 100, ResumeAtBytes: 200})
	if status := d.snapshot(); !status.Paused || status.Reason != "disk_not_checked" || status.AvailableBytes != nil {
		t.Fatalf("startup admitted work: %+v", status)
	}
	probeErr := errors.New("unavailable mount")
	for i, tc := range []struct {
		name      string
		available uint64
		err       error
		paused    bool
	}{
		{"startup in recovery band", 150, nil, true},
		{"resume at threshold", 200, nil, false},
		{"healthy band", 150, nil, false},
		{"pause boundary still healthy", 100, nil, false},
		{"below pause threshold", 99, nil, true},
		{"pause boundary still paused", 100, nil, true},
		{"below resume threshold", 199, nil, true},
		{"recover", 200, nil, false},
		{"empty filesystem", 0, nil, true},
		{"recover from zero", 300, nil, false},
		{"probe failure", 300, probeErr, true},
		{"failure also needs recovery threshold", 150, nil, true},
		{"recover from failure", 200, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d.probe = func(ctx context.Context, dir string) (uint64, error) {
				if ctx != t.Context() || dir != "/data" {
					t.Fatal("probe lost context or data directory")
				}
				return tc.available, tc.err
			}
			at := time.Unix(int64(i), 0).UTC()
			if err := d.check(t.Context(), at); !errors.Is(err, tc.err) {
				t.Fatalf("check error=%v want=%v", err, tc.err)
			}
			status := d.snapshot()
			if status.Paused != tc.paused || !status.CheckedAt.Equal(at) {
				t.Fatalf("wrong status: %+v", status)
			}
			if tc.err != nil {
				if status.Reason != "disk_check_failed" || status.AvailableBytes != nil || status.Error == "" {
					t.Fatalf("failure reported stale capacity: %+v", status)
				}
			} else if status.AvailableBytes == nil || *status.AvailableBytes != tc.available || status.Error != "" || (status.Reason == "disk_low") != tc.paused {
				t.Fatalf("wrong measurement: %+v", status)
			}
		})
	}
}

func TestDiskStatusConcurrentReads(t *testing.T) {
	d := newDiskGuard(t.TempDir(), DefaultDiskSpace())
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 100 {
			if _, err := json.Marshal(d.snapshot()); err != nil {
				t.Error(err)
			}
		}
	})
	for range 100 {
		if err := d.check(t.Context(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}

func TestDiskGuardRequiresDataDirectoryAndValidThresholds(t *testing.T) {
	h := newHarness(t, 1)
	for _, policy := range []DiskSpace{{PauseBelowBytes: 1}, {ResumeAtBytes: 2}, {PauseBelowBytes: 2, ResumeAtBytes: 2}, {PauseBelowBytes: 3, ResumeAtBytes: 2}} {
		cfg := h.cfg
		cfg.DiskSpace = policy
		if _, err := New(cfg, h.store, h.runtime, h.provider); err == nil {
			t.Fatalf("invalid policy accepted: %+v", policy)
		}
	}
	cfg := h.cfg
	cfg.DataDir = ""
	if _, err := New(cfg, h.store, h.runtime, h.provider); err == nil {
		t.Fatal("missing data directory accepted")
	}
}

func diskStatus(t *testing.T, h *harness) DiskStatus {
	t.Helper()
	code, body := h.admin(http.MethodGet, "/api/status", nil)
	var status struct {
		Disk DiskStatus `json:"disk"`
	}
	if err := json.Unmarshal(body, &status); err != nil || code != http.StatusOK {
		t.Fatalf("disk status: %d %s %v", code, body, err)
	}
	return status.Disk
}

func TestDiskPauseKeepsResultsCancellationChecksAndMaintenance(t *testing.T) {
	for _, failProbe := range []bool{false, true} {
		name := "low space"
		if failProbe {
			name = "probe failure"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, 2)
			finished := h.manual(testWorkflow)
			cancelled := h.manual(testWorkflow)
			h.tick()
			queued := h.manual(testWorkflow)
			discarded := h.manual(testWorkflow)
			probeErr := errors.New("disk temporarily unavailable")
			h.server.disk.probe = func(context.Context, string) (uint64, error) {
				if failProbe {
					return 0, probeErr
				}
				return 1, nil
			}
			pausedTick := func() {
				t.Helper()
				err := h.server.Tick(t.Context())
				if (err != nil) != failProbe || (failProbe && !errors.Is(err, probeErr)) {
					t.Fatalf("tick: %v", err)
				}
			}
			pausedTick()
			if status := diskStatus(t, h); !status.Paused {
				t.Fatalf("not paused: %+v", status)
			}
			if code, body := h.admin(http.MethodGet, "/api/status", nil); code != 200 || !strings.Contains(string(body), `"paused":true`) {
				t.Fatalf("missing pause notice: %d %s", code, body)
			}
			if code, _ := h.request(http.MethodGet, "/api/status", "", nil); code != http.StatusUnauthorized {
				t.Fatalf("status exposed without auth: %d", code)
			}
			if code, _ := h.request(http.MethodGet, "/healthz", "", nil); code != 200 {
				t.Fatalf("pressure triggered unhealthy process: %d", code)
			}
			h.complete(finished.Jobs[0]) // Logs and terminal results remain writable.
			for _, id := range []string{cancelled.ID, discarded.ID} {
				if code, body := h.admin(http.MethodPost, "/api/runs/"+id+"/cancel", nil); code != 200 {
					t.Fatalf("cancel: %d %s", code, body)
				}
			}
			backup := filepath.Join(h.cfg.DataDir, "backup-"+strings.Repeat("a", 32)+".db")
			if err := os.WriteFile(backup, []byte("expired snapshot"), 0600); err != nil {
				t.Fatal(err)
			}
			old := time.Now().Add(-8 * 24 * time.Hour)
			if err := os.Chtimes(backup, old, old); err != nil {
				t.Fatal(err)
			}
			h.server.nextMaintenance = time.Time{}
			pausedTick()
			pausedTick() // Cancellation is acknowledged after the Pod disappears.
			for id, want := range map[string]run.State{finished.ID: run.Succeeded, cancelled.ID: run.Cancelled, discarded.ID: run.Cancelled, queued.ID: run.Queued} {
				got, err := h.store.GetRun(t.Context(), id)
				if err != nil || got.State != want {
					t.Fatalf("run %s: %+v %v want %s", id, got, err, want)
				}
			}
			if _, err := os.Stat(backup); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("maintenance stopped under pressure: %v", err)
			}
			if checks, err := h.store.PendingChecks(t.Context()); err != nil || len(checks) != 0 {
				t.Fatalf("Checks stalled: %v %v", checks, err)
			}
			if h.runtime.created != 2 || len(h.runtime.pods) != 0 {
				t.Fatalf("admission or GC failed: %+v", h.runtime)
			}
			h.server.disk.probe = func(context.Context, string) (uint64, error) { return 2 << 30, nil }
			h.tick()
			if h.runtime.created != 3 || diskStatus(t, h).Paused {
				t.Fatal("queued job did not resume")
			}
			h.complete(queued.Jobs[0])
			h.tick()
			if code, body := h.admin(http.MethodGet, "/api/status", nil); code != 200 || !strings.Contains(string(body), `"paused":false`) {
				t.Fatalf("stale pause notice: %d %s", code, body)
			}
		})
	}
}

func TestDiskPauseRestartAdoptsExistingPodAndDefersReservedAttempt(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "reserved without pod"
		if existing {
			name = "created pod with lost response"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, 1)
			item := h.manual(testWorkflow)
			jobs, err := h.store.Reserve(t.Context(), 1)
			if err != nil || len(jobs) != 1 {
				t.Fatalf("reserve: %v %v", jobs, err)
			}
			if existing {
				if _, err := h.runtime.Ensure(t.Context(), jobs[0], h.cfg.Profiles["small"]); err != nil {
					t.Fatal(err)
				}
			}
			h.http.Close()
			if err := h.store.Close(); err != nil {
				t.Fatal(err)
			}
			h.store, err = storage.Open(t.Context(), h.path)
			if err != nil {
				t.Fatal(err)
			}
			h.start()
			// Restart in the hysteresis band must not bypass a pause.
			h.server.disk.probe = func(context.Context, string) (uint64, error) { return 768 << 20, nil }
			h.tick()
			job, err := h.store.GetJob(t.Context(), item.Jobs[0].ID)
			if err != nil || !diskStatus(t, h).Paused || (job.PodUID != "") != existing {
				t.Fatalf("recovery: %+v %v", job, err)
			}
			if !existing && h.runtime.created != 0 {
				t.Fatal("created reserved Pod while paused")
			}
			h.server.disk.probe = func(context.Context, string) (uint64, error) { return 1 << 30, nil }
			h.tick()
			h.tick()
			if h.runtime.created != 1 {
				t.Fatalf("attempt recreated: %d", h.runtime.created)
			}
			h.complete(job)
			h.tick()
		})
	}
}

func TestDiskPressureBetweenReservationAndPodCreation(t *testing.T) {
	h := newHarness(t, 1)
	item := h.manual(testWorkflow)
	checks := 0
	h.server.disk.probe = func(context.Context, string) (uint64, error) {
		checks++
		if checks == 1 {
			return 2 << 30, nil
		}
		return 1, nil
	}
	h.tick()
	h.tick()
	job, err := h.store.GetJob(t.Context(), item.Jobs[0].ID)
	if err != nil || job.State != run.Starting || h.runtime.created != 0 {
		t.Fatalf("mid-tick pressure admitted Pod: %+v %v", job, err)
	}
	h.server.disk.probe = func(context.Context, string) (uint64, error) { return 1 << 30, nil }
	h.tick()
	if h.runtime.created != 1 {
		t.Fatal("reserved attempt not resumed")
	}
}
