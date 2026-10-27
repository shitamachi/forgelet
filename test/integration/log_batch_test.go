//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/testcontainers/testcontainers-go"
	"k8s.io/client-go/kubernetes"

	"github.com/shitamachi/forgelet/internal/run"
)

func testBatchedLogRestart(t *testing.T, ctx context.Context, cluster testcontainers.Container, client *kubernetes.Clientset, httpClient *http.Client, endpoint string, create func(*testing.T, string) run.Run, get func(*testing.T, string) run.Run, request func(*testing.T, string, string, any, int) []byte) {
	t.Run("batched Unicode logs survive restart without loss or duplicate sequences", func(t *testing.T) {
		item := create(t, `on: push
jobs:
  stream:
    runs-on: homelab
    steps:
      - id: stream
        run: |
          printf '::add-mask::batch-private-value\n'
          echo batch-begin
          i=0
          while [ ! -f /workspace/repository/continue ]; do
            printf '批次-%s 🚀 batch-private-value\n' "$i"
            i=$((i+1))
            sleep 0.02
          done
          printf 'batch-count=%s\n' "$i"
`)
		logs := func() ([]run.Log, string) {
			var all []run.Log
			var text strings.Builder
			var after int64
			for {
				var page []run.Log
				must(t, json.Unmarshal(request(t, "GET", fmt.Sprintf("/api/jobs/%s/logs?after=%d", item.Jobs[0].ID, after), nil, http.StatusOK), &page))
				if len(page) == 0 {
					return all, text.String()
				}
				for _, entry := range page {
					all = append(all, entry)
					text.WriteString(entry.Data)
					after = entry.Seq
				}
			}
		}
		waitFor(t, ctx, "batched stream visible while running", func() bool { _, s := logs(); return strings.Contains(s, "批次-") })
		before := get(t, item.ID).Jobs[0]
		restartServer(t, ctx, client, httpClient, endpoint)
		after := get(t, item.ID).Jobs[0]
		if before.PodUID == "" || after.PodUID != before.PodUID || after.State != run.Running {
			t.Fatal("log stream attempt replaced on restart", after)
		}
		execInPod(t, ctx, cluster, before.PodName, "touch /workspace/repository/continue")
		waitFor(t, ctx, "stream completed after reconnect", func() bool { return get(t, item.ID).State.Terminal() })
		result := get(t, item.ID).Jobs[0]
		entries, text := logs()
		if result.State != run.Succeeded || result.Result.LastLogSeq != int64(len(entries)) || strings.Contains(text, "batch-private-value") || strings.Count(text, "batch-begin\n") != 1 {
			t.Fatalf("unsafe stream recovery: %+v %s", result, text)
		}
		for i, e := range entries {
			if e.Seq != int64(i+1) || len(e.Data) > 8<<10 || !utf8.ValidString(e.Data) || e.StepID != "stream" {
				t.Fatalf("bad acknowledged chunk %d", i)
			}
		}
		count := regexp.MustCompile(`(?m)^batch-count=(\d+)$`).FindStringSubmatch(text)
		if len(count) != 2 {
			t.Fatal("missing exact output count", text)
		}
		n, err := strconv.Atoi(count[1])
		must(t, err)
		lines := regexp.MustCompile(`(?m)^批次-(\d+) 🚀 \*\*\*$`).FindAllStringSubmatch(text, -1)
		if len(lines) != n || n == 0 {
			t.Fatalf("stream missing/duplicated lines: expected %d got %d", n, len(lines))
		}
		for i, line := range lines {
			if line[1] != strconv.Itoa(i) {
				t.Fatalf("stream out of order at %d: %v", i, line)
			}
		}
	})
}
