package server_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/server"
	"github.com/shitamachi/forgelet/internal/storage/memory"
)

// The /metrics endpoint exposes dispatch latency and queue depth populated
// by the driver loops (spec 0010 T3).
func TestMetricsEndpoint(t *testing.T) {
	ctx := context.Background()
	workflows := t.TempDir()
	if err := os.WriteFile(filepath.Join(workflows, "ci.yml"), []byte(e2eWorkflow), 0o644); err != nil {
		t.Fatal(err)
	}
	durable := memory.NewDurableStore(func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, nil)
	srv, err := server.NewServer(server.Options{
		WebhookSecret: []byte("whsec"),
		WorkflowsDir:  workflows,
		CheckReporter: &capturedReporter{},
		Durable:       durable,
		TokenKey:      bytes.Repeat([]byte{0x42}, 32),
		Now:           func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		Log:           slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := postEvent(t, ctx, ts, "push", "metrics-d1", pushPayload)
	if resp.code != http.StatusOK {
		t.Fatalf("webhook: %d %s", resp.code, resp.body)
	}
	// Queue depth is published on every dispatch drain; two queued jobs go
	// to zero after this call.
	if n, err := srv.DispatchOnce(ctx); err != nil || n != 2 {
		t.Fatalf("dispatch: n=%d err=%v", n, err)
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/metrics", nil)
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	text := string(body)
	for _, want := range []string{
		"forgelet_dispatch_duration_seconds",
		"forgelet_jobrun_queue_depth 0",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
}
