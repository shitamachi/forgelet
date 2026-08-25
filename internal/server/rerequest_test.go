package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/server"
	"github.com/shitamachi/forgelet/internal/storage/memory"
)

func TestRerequestCreatesNewAttempt(t *testing.T) {
	ctx := context.Background()
	workflows := t.TempDir()
	// Simple workflow with one job
	if err := os.WriteFile(filepath.Join(workflows, "ci.yml"), []byte("name: CI\non: push\njobs:\n  test:\n    runs-on: small\n    steps:\n      - run: exit 1\n"), 0o644); err != nil {
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

	// Ingest push
	resp := postEvent(t, ctx, ts, "push", "rereq-d1", pushPayload)
	if resp.code != 200 {
		t.Fatalf("webhook: %d %s", resp.code, resp.body)
	}
	run := durable.Runs()[0]
	jobs, _ := durable.ListJobRuns(ctx, run.ID)
	if len(jobs) != 1 {
		t.Fatalf("jobs=%d", len(jobs))
	}
	origID := jobs[0].ID
	// Dispatch and fail
	if _, err := srv.DispatchOnce(ctx); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := durable.ApplyObserved(ctx, origID, model.PhaseFailed, time.Unix(1_700_000_100, 0).UTC()); err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	// Rerequest via check_run webhook
	payload := `{"action":"rerequested","check_run":{"external_id":"` + string(origID) + `","head_sha":"abc"}}`
	sig := signBody("whsec", payload)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/webhooks/github", bytes.NewReader([]byte(payload)))
	req.Header.Set("X-GitHub-Event", "check_run")
	req.Header.Set("X-GitHub-Delivery", "rereq-d2")
	req.Header.Set("X-Hub-Signature-256", sig)
	req.Header.Set("Content-Type", "application/json")
	httpResp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("rerequest webhook: %v", err)
	}
	body, _ := io.ReadAll(httpResp.Body)
	_ = httpResp.Body.Close()
	if httpResp.StatusCode != 200 {
		t.Fatalf("rerequest: %d %s", httpResp.StatusCode, body)
	}
	var out map[string]string
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	newID := model.JobRunID(out["newJobRunId"])
	if newID == "" || newID == origID {
		t.Fatalf("newJobRunId = %q", out["newJobRunId"])
	}
	// New attempt should be queued and dispatchable
	jobs, _ = durable.ListJobRuns(ctx, run.ID)
	if len(jobs) != 2 {
		t.Fatalf("after rerequest jobs=%d", len(jobs))
	}
	var newRec *model.JobRunRecord
	for i, j := range jobs {
		if j.ID == newID {
			newRec = &jobs[i]
			break
		}
	}
	if newRec == nil {
		t.Fatal("new job not found")
	}
	if newRec.Attempt != 2 || newRec.Status != model.JobQueued {
		t.Errorf("new job attempt=%d status=%s, want 2 queued", newRec.Attempt, newRec.Status)
	}
	// Run should be reopened (not terminal)
	runRec, _ := durable.GetRun(ctx, run.ID)
	if runRec.Status.IsTerminal() {
		t.Errorf("run should be reopened, got %s", runRec.Status)
	}
	// Dispatch should now get the new attempt
	n, err := srv.DispatchOnce(ctx)
	if err != nil || n != 1 {
		t.Fatalf("dispatch after rerequest: n=%d err=%v", n, err)
	}
	// Idempotency: same delivery should return same newID, not create another
	req2, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/webhooks/github", bytes.NewReader([]byte(payload)))
	req2.Header.Set("X-GitHub-Event", "check_run")
	req2.Header.Set("X-GitHub-Delivery", "rereq-d2")
	req2.Header.Set("X-Hub-Signature-256", sig)
	req2.Header.Set("Content-Type", "application/json")
	resp2, _ := ts.Client().Do(req2)
	body2, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	var out2 map[string]string
	_ = json.Unmarshal(body2, &out2)
	if out2["newJobRunId"] != string(newID) {
		t.Errorf("idempotent rerequest: got %q want %q", out2["newJobRunId"], newID)
	}
	jobs, _ = durable.ListJobRuns(ctx, run.ID)
	if len(jobs) != 2 {
		t.Errorf("after idempotent rerequest jobs=%d, want 2", len(jobs))
	}
}
