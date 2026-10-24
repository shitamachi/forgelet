package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestRunRemoteRetriesCommittedResultAfterResponseLoss(t *testing.T) {
	var mu sync.Mutex
	var saved []byte
	resultCalls, commits, claims := 0, 0, 0
	plan := remoteTestPlan()
	plan.Steps[0].Run = "echo once >> executions"
	options := remoteTestOptions(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/claim"):
			claims++
			_ = json.NewEncoder(w).Encode(plan)
		case strings.HasSuffix(r.URL.Path, "/control"):
			_, _ = w.Write([]byte(`{"cancel_requested":false}`))
		case strings.HasSuffix(r.URL.Path, "/result"):
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			resultCalls++
			if saved == nil {
				saved = append([]byte(nil), body...)
				commits++
				// Persist first, then lose the connection before any response can
				// reach the executor. Its retry must carry the identical result.
				connection, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = connection.Close()
				return
			}
			if !bytes.Equal(saved, body) {
				t.Error("result payload changed after response loss")
				http.Error(w, "conflict", http.StatusConflict)
				return
			}
			_, _ = w.Write([]byte(`{"saved":true}`))
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	if err := RunRemote(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	var result run.Result
	if err := json.Unmarshal(saved, &result); err != nil {
		t.Fatal(err)
	}
	if claims != 1 || commits != 1 || resultCalls != 2 || result.State != run.Succeeded {
		t.Fatalf("claims=%d commits=%d attempts=%d result=%+v", claims, commits, resultCalls, result)
	}
	data, err := os.ReadFile(filepath.Join(options.Workspace, "executions"))
	if err != nil || string(data) != "once\n" {
		t.Fatalf("execution repeated: %q error=%v", data, err)
	}
}

func TestRunRemoteCancellationFlushesEntireLargeChunk(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var mu sync.Mutex
	entries := make(map[int64]string)
	var result run.Result
	first := true
	plan := remoteTestPlan()
	plan.Steps[0].Run = "printf '%20000s\\n' x\nsleep 30"
	options := remoteTestOptions(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/claim"):
			_ = json.NewEncoder(w).Encode(plan)
		case strings.HasSuffix(r.URL.Path, "/control"):
			_, _ = w.Write([]byte(`{"cancel_requested":false}`))
		case strings.HasSuffix(r.URL.Path, "/logs"):
			var entry run.Log
			if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
				t.Error(err)
				return
			}
			if previous, exists := entries[entry.Seq]; exists && previous != entry.Data {
				t.Error("log payload changed during retry")
			}
			entries[entry.Seq] = entry.Data
			if first {
				first = false
				cancel()
				http.Error(w, "response lost", http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]int64{"seq": entry.Seq})
		case strings.HasSuffix(r.URL.Path, "/result"):
			if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
				t.Error(err)
			}
			_, _ = w.Write([]byte(`{"saved":true}`))
		default:
			http.NotFound(w, r)
		}
	})
	if err := RunRemote(ctx, options); err == nil {
		t.Fatal("cancelled execution must return an error")
	}
	mu.Lock()
	defer mu.Unlock()
	var combined strings.Builder
	for seq := int64(1); seq <= result.LastLogSeq; seq++ {
		combined.WriteString(entries[seq])
	}
	want := strings.Repeat(" ", 19999) + "x\n"
	if result.State != run.Cancelled || combined.String() != want || result.LastLogSeq < 3 {
		t.Fatalf("result=%+v log bytes=%d want=%d", result, combined.Len(), len(want))
	}
}
