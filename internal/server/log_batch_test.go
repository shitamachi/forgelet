package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/shitamachi/forgelet/internal/executor"
	"github.com/shitamachi/forgelet/internal/run"
)

func TestBatchedExecutorLogsUseRealHTTPAndSQLiteACK(t *testing.T) {
	h := newHarness(t, 1)
	item := h.manual(`on: push
jobs:
  test:
    runs-on: small
    steps:
      - id: first
        run: |
          printf '::add-mask::log-private-value\n'
          i=0; while [ "$i" -lt 2000 ]; do printf '执行 🚀 log-private-value\n'; i=$((i+1)); done
      - id: last
        run: printf 'last-step\n'
`)
	h.tick()
	job, err := h.store.GetJob(t.Context(), item.Jobs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(t.TempDir(), "token")
	if err = os.WriteFile(token, []byte("token-"+job.ID), 0600); err != nil {
		t.Fatal(err)
	}
	if err = executor.RunRemote(t.Context(), executor.RemoteOptions{URL: h.http.URL, JobID: job.ID, TokenFile: token, Workspace: t.TempDir(), Client: h.http.Client(), PollInterval: 10 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	current, err := h.store.GetJob(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	logs, err := h.store.Logs(t.Context(), job.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for i, entry := range logs {
		if entry.Seq != int64(i+1) || !utf8.ValidString(entry.Data) {
			t.Fatal("bad stored sequence/UTF8")
		}
		text.WriteString(entry.Data)
	}
	if current.State != run.Succeeded || current.Result.LastLogSeq != int64(len(logs)) || len(logs) > 20 {
		t.Fatalf("incorrect ACK or inefficient batching: state=%s entries=%d", current.State, len(logs))
	}
	if text.String() != strings.Repeat("执行 🚀 ***\n", 2000)+"last-step\n" || logs[len(logs)-1].StepID != "last" {
		t.Fatal("lost output, secret masking or step attribution")
	}
}
