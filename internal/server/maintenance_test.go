package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run"
	"github.com/shitamachi/forgelet/internal/storage"
)

func TestRetentionExpiresLogsBeforeHistoryAndKeepsActiveWork(t *testing.T) {
	h := newHarness(t, 4)
	old := h.manual(testWorkflow)
	h.tick()
	current, err := h.store.GetRun(t.Context(), old.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range current.Jobs {
		h.complete(job)
	}
	h.tick() // Send terminal Checks and remove execution Pods before retention.
	active := h.manual(testWorkflow)
	h.tick()
	base := time.Now().UTC()
	h.server.now = func() time.Time { return base.Add(8 * 24 * time.Hour) }
	h.tick()
	for _, job := range current.Jobs {
		for _, accept := range []string{"application/json", "text/event-stream"} {
			status, body := h.request(http.MethodGet, "/api/jobs/"+job.ID+"/logs", "", map[string]string{"Authorization": "Bearer " + testAdmin, "Accept": accept})
			if status != http.StatusGone || !strings.Contains(string(body), "expired") {
				t.Fatalf("expired %s logs: %d %s", accept, status, body)
			}
		}
	}
	if status, body := h.admin(http.MethodGet, "/api/runs/"+old.ID, nil); status != http.StatusOK || !strings.Contains(string(body), `"logs_expired":true`) {
		t.Fatalf("history page after log expiration: %d %s", status, body)
	}
	h.server.now = func() time.Time { return base.Add(31 * 24 * time.Hour) }
	if status, body := h.admin(http.MethodPost, "/api/maintenance", nil); status != http.StatusOK {
		t.Fatalf("maintenance: %d %s", status, body)
	}
	if status, _ := h.admin(http.MethodGet, "/api/runs/"+old.ID, nil); status != http.StatusNotFound {
		t.Fatalf("expired history returned %d", status)
	}
	live, err := h.store.GetRun(t.Context(), active.ID)
	if err != nil || live.State.Terminal() {
		t.Fatalf("active run was pruned: %+v %v", live, err)
	}
	if status, _ := h.request(http.MethodPost, "/api/maintenance", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated maintenance returned %d", status)
	}
}

func TestPruneBackupsKeepsUnrelatedFilesAndSymlinks(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	old := now.Add(-48 * time.Hour)
	name := func(c string) string { return "backup-" + strings.Repeat(c, 32) + ".db" }
	for _, file := range []string{name("a"), name("b"), "forgelet.db", "backup-in-progress.tmp"} {
		path := filepath.Join(dir, file)
		if err := os.WriteFile(path, []byte("keep unless expired snapshot"), 0600); err != nil {
			t.Fatal(err)
		}
		if file != name("b") {
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.Symlink(filepath.Join(dir, "forgelet.db"), filepath.Join(dir, name("c"))); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, name("d")), 0700); err != nil {
		t.Fatal(err)
	}
	n, err := pruneBackups(t.Context(), dir, now.Add(-24*time.Hour), 100)
	if err != nil || n != 1 {
		t.Fatalf("prune=%d %v", n, err)
	}
	for _, file := range []string{name("b"), name("c"), name("d"), "forgelet.db", "backup-in-progress.tmp"} {
		if _, err := os.Lstat(filepath.Join(dir, file)); err != nil {
			t.Fatalf("removed unrelated file %s: %v", file, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := pruneBackups(ctx, dir, now, 100); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled prune: %v", err)
	}
}

func TestExpiredDeliveryCannotCreateNewExecution(t *testing.T) {
	h := newHarness(t, 4)
	if status, body := h.webhook("retained-delivery", nil); status != http.StatusAccepted {
		t.Fatalf("webhook: %d %s", status, body)
	}
	h.tick()
	runs, err := h.store.ListRuns(t.Context())
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%v %v", runs, err)
	}
	for _, job := range runs[0].Jobs {
		h.complete(job)
	}
	h.tick()
	h.server.now = func() time.Time { return time.Now().Add(31 * 24 * time.Hour) }
	if status, body := h.admin(http.MethodPost, "/api/maintenance", nil); status != http.StatusOK {
		t.Fatalf("maintenance: %d %s", status, body)
	}
	if _, err := h.store.GetRun(t.Context(), runs[0].ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("run not expired: %v", err)
	}
	if status, _ := h.webhook("retained-delivery", nil); status != http.StatusAccepted {
		t.Fatalf("redelivery: %d", status)
	}
	h.tick()
	if items, err := h.store.ListRuns(t.Context()); err != nil || len(items) != 0 {
		t.Fatalf("redelivery replayed expired work: %v %v", items, err)
	}
	// Even ingestion of the same immutable plan through the storage boundary
	// must recognize the tombstone rather than recreating a run.
	_, err = h.server.createRun(t.Context(), run.Event{DeliveryID: "retained-delivery", Repository: "me/repo", SHA: testSHA}, ".github/workflows/ci.yml", []byte(testWorkflow))
	if !errors.Is(err, errRunExpired) {
		t.Fatalf("expired create did not recognize tombstone: %v", err)
	}
}
