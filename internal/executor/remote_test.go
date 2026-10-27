package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/shitamachi/forgelet/internal/run"
)

func remoteTestOptions(t *testing.T, handler http.HandlerFunc) RemoteOptions {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	workspace := t.TempDir()
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("token-one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return RemoteOptions{URL: server.URL, JobID: "job-1", PodUID: "pod-uid", TokenFile: tokenPath, Workspace: workspace, Client: server.Client(), RetryTimeout: time.Second, PollInterval: 10 * time.Millisecond, FlushTimeout: time.Second}
}

func TestRemoteRequestRetriesSamePayloadAndReadsRotatedToken(t *testing.T) {
	var mu sync.Mutex
	var bodies []run.Log
	var auth []string
	var tokenFile string
	options := remoteTestOptions(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		auth = append(auth, r.Header.Get("Authorization"))
		var entry run.Log
		if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
			t.Error(err)
		}
		bodies = append(bodies, entry)
		if len(bodies) == 1 {
			if err := os.WriteFile(tokenFile, []byte("token-two"), 0600); err != nil {
				t.Error(err)
			}
			http.Error(w, "secret-server-body", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"seq":1}`))
	})
	tokenFile = options.TokenFile
	client, err := newRemoteClient(options)
	if err != nil {
		t.Fatal(err)
	}
	entry := run.Log{Seq: 1, StepID: "build", Data: "line\n"}
	var response struct {
		Seq int64 `json:"seq"`
	}
	if err := client.request(t.Context(), http.MethodPost, "/logs", entry, &response); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 || bodies[0] != entry || bodies[1] != entry || response.Seq != 1 {
		t.Fatalf("requests=%v response=%+v", bodies, response)
	}
	if auth[0] != "Bearer token-one" || auth[1] != "Bearer token-two" {
		t.Fatalf("token rotation=%v", auth)
	}
}

func TestRemoteRejectsPermanentFailuresWithoutLeakingBody(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusBadRequest} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			count := 0
			options := remoteTestOptions(t, func(w http.ResponseWriter, r *http.Request) {
				count++
				http.Error(w, "token-one secret-server-body", status)
			})
			client, err := newRemoteClient(options)
			if err != nil {
				t.Fatal(err)
			}
			err = client.request(t.Context(), http.MethodPost, "/claim", nil, nil)
			if err == nil || strings.Contains(err.Error(), "token-one") || strings.Contains(err.Error(), "secret-server-body") {
				t.Fatalf("unsafe error=%v", err)
			}
			if count != 1 {
				t.Fatalf("permanent error retried %d times", count)
			}
		})
	}
}

func TestRemoteRetryStopsOnCancellation(t *testing.T) {
	options := remoteTestOptions(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	})
	client, err := newRemoteClient(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if err := client.request(ctx, http.MethodGet, "/control", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation=%v", err)
	}
}

func TestRemoteRejectsRedirectsAndInvalidToken(t *testing.T) {
	forwarded := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded = true }))
	t.Cleanup(target.Close)
	options := remoteTestOptions(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	})
	client, err := newRemoteClient(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.request(t.Context(), http.MethodPost, "/claim", nil, nil); err == nil {
		t.Fatal("redirect accepted")
	}
	if forwarded {
		t.Fatal("token forwarded")
	}
	if err := os.WriteFile(options.TokenFile, []byte(strings.Repeat("x", 16385)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := client.request(t.Context(), http.MethodGet, "/control", nil, nil); err == nil {
		t.Fatal("oversized token accepted")
	}
}

func TestRemoteConfigurationAndResponseValidation(t *testing.T) {
	options := remoteTestOptions(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`not JSON containing token-one`)) })
	client, err := newRemoteClient(options)
	if err != nil {
		t.Fatal(err)
	}
	var response any
	if err := client.request(t.Context(), http.MethodGet, "/control", nil, &response); err == nil || strings.Contains(err.Error(), "token-one") {
		t.Fatalf("malformed response=%v", err)
	}
	for _, bad := range []string{"", "file:///etc/passwd", "https://user:token@host", "https://host/?token=x"} {
		copyOptions := options
		copyOptions.URL = bad
		if _, err := newRemoteClient(copyOptions); err == nil {
			t.Fatalf("accepted URL %q", bad)
		}
	}
	options.JobID = "../../other-job"
	if _, err := newRemoteClient(options); err == nil {
		t.Fatal("accepted job path escape")
	}
}

func remoteTestPlan() run.Plan {
	return run.Plan{JobID: "job-1", RunID: "run-1", Repository: "owner/repo", SHA: strings.Repeat("a", 40), Ref: "refs/heads/main", TimeoutSeconds: 10, Steps: []run.Step{{ID: "build", Shell: "sh", Run: "printf 'hello\\n'"}}}
}

func TestRunRemoteExecutesAndReportsAcknowledgedSequence(t *testing.T) {
	var mu sync.Mutex
	var logs []run.Log
	var result run.Result
	resultCalls := 0
	options := remoteTestOptions(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/claim"):
			_ = json.NewEncoder(w).Encode(remoteTestPlan())
		case strings.HasSuffix(r.URL.Path, "/control"):
			_, _ = w.Write([]byte(`{"cancel_requested":false}`))
		case strings.HasSuffix(r.URL.Path, "/logs"):
			var entry run.Log
			if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
				t.Error(err)
			}
			logs = append(logs, entry)
			_ = json.NewEncoder(w).Encode(map[string]int64{"seq": entry.Seq})
		case strings.HasSuffix(r.URL.Path, "/result"):
			if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
				t.Error(err)
			}
			resultCalls++
			if resultCalls == 1 {
				http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(`{"saved":true}`))
		default:
			t.Errorf("unexpected endpoint: %s", r.URL)
			http.NotFound(w, r)
		}
	})
	if err := RunRemote(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logs) != 1 || logs[0].Seq != 1 || logs[0].StepID != "build" || logs[0].Data != "hello\n" {
		t.Fatalf("logs=%+v", logs)
	}
	if result.State != run.Succeeded || result.LastLogSeq != 1 || resultCalls != 2 {
		t.Fatalf("result=%+v calls=%d", result, resultCalls)
	}
}

func TestRunRemoteControlCancelsExecutionAndSavesResult(t *testing.T) {
	var mu sync.Mutex
	started := false
	var result run.Result
	options := remoteTestOptions(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/claim"):
			plan := remoteTestPlan()
			plan.Steps[0].Run = "printf 'started\\n'; sleep 30; touch should-not-exist"
			_ = json.NewEncoder(w).Encode(plan)
		case strings.HasSuffix(r.URL.Path, "/control"):
			_ = json.NewEncoder(w).Encode(map[string]bool{"cancel_requested": started})
		case strings.HasSuffix(r.URL.Path, "/logs"):
			var entry run.Log
			if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
				t.Error(err)
			}
			started = true
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
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := RunRemote(ctx, options); err == nil {
		t.Fatal("cancelled execution returned success")
	}
	mu.Lock()
	defer mu.Unlock()
	if result.State != run.Cancelled || result.LastLogSeq < 1 {
		t.Fatalf("cancelled result=%+v", result)
	}
	if _, err := os.Stat(filepath.Join(options.Workspace, "should-not-exist")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled process continued: %v", err)
	}
}

func TestRunRemoteFlushesSameLogAfterParentCancellation(t *testing.T) {
	var mu sync.Mutex
	var logs []run.Log
	var result run.Result
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	options := remoteTestOptions(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/claim"):
			plan := remoteTestPlan()
			plan.Steps[0].Run = "printf 'started\\n'; sleep 30"
			_ = json.NewEncoder(w).Encode(plan)
		case strings.HasSuffix(r.URL.Path, "/control"):
			_, _ = w.Write([]byte(`{"cancel_requested":false}`))
		case strings.HasSuffix(r.URL.Path, "/logs"):
			var entry run.Log
			if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
				t.Error(err)
			}
			logs = append(logs, entry)
			if len(logs) == 1 {
				cancel()
				http.Error(w, "ACK lost", http.StatusServiceUnavailable)
				return
			}
			// The parent is already cancelled. A slow sink must still receive
			// the identical pending chunk under the independent flush context.
			select {
			case <-time.After(150 * time.Millisecond):
			case <-r.Context().Done():
				t.Error("flush inherited cancelled request")
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
		t.Fatal("cancelled execution returned success")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logs) != 2 || logs[0] != logs[1] {
		t.Fatalf("log retry changed payload: %+v", logs)
	}
	if result.State != run.Cancelled || result.LastLogSeq != 1 {
		t.Fatalf("final result=%+v", result)
	}
}

func TestRunRemoteControlFailureReportsAcceptedTerminalState(t *testing.T) {
	var mu sync.Mutex
	var result run.Result
	saved := false
	options := remoteTestOptions(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/claim"):
			plan := remoteTestPlan()
			plan.Steps[0].Run = "sleep 30"
			_ = json.NewEncoder(w).Encode(plan)
		case strings.HasSuffix(r.URL.Path, "/control"):
			http.Error(w, "private-control-error", http.StatusUnauthorized)
		case strings.HasSuffix(r.URL.Path, "/logs"):
			var entry run.Log
			if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]int64{"seq": entry.Seq})
		case strings.HasSuffix(r.URL.Path, "/result"):
			if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
				t.Error(err)
			}
			// Match the terminal-state contract of storage.Finish: interrupted
			// is assigned by the control plane, never by an executor report.
			switch result.State {
			case run.Succeeded, run.Failed, run.Cancelled, run.TimedOut:
				saved = true
				_, _ = w.Write([]byte(`{"saved":true}`))
			default:
				http.Error(w, "invalid executor terminal state", http.StatusConflict)
			}
		default:
			http.NotFound(w, r)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err := RunRemote(ctx, options)
	if err == nil || !strings.Contains(err.Error(), "cancellation polling failed") || strings.Contains(err.Error(), "private-control-error") {
		t.Fatalf("control failure: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !saved || result.State != run.Failed || result.Error != "control-plane cancellation status unavailable" {
		t.Fatalf("saved=%v result=%+v", saved, result)
	}
}

func TestCredentialRejectionConfirmsCancellationWithoutStartingCode(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		credentialStatus, controlStatus int
		cancel                          bool
		want                            run.State
	}{
		{"cancel between claim and credentials", 409, 200, true, run.Cancelled},
		{"terminating original pod", 401, 200, true, run.Cancelled},
		{"genuine credential failure", 403, 200, false, run.Failed},
		{"unconfirmed cancellation remains failure", 401, 401, false, run.Failed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var result run.Result
			executed := false
			options := remoteTestOptions(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch {
				case strings.HasSuffix(r.URL.Path, "/claim"):
					plan := remoteTestPlan()
					plan.SecretNames = []string{"TOKEN"}
					_ = json.NewEncoder(w).Encode(plan)
				case strings.HasSuffix(r.URL.Path, "/credentials"):
					http.Error(w, "denied", tc.credentialStatus)
				case strings.HasSuffix(r.URL.Path, "/control"):
					if tc.controlStatus != 200 {
						http.Error(w, "invalid identity", tc.controlStatus)
					} else {
						_ = json.NewEncoder(w).Encode(map[string]bool{"cancel_requested": tc.cancel})
					}
				case strings.HasSuffix(r.URL.Path, "/result"):
					if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
						t.Error(err)
					}
					_, _ = w.Write([]byte(`{"saved":true}`))
				case strings.HasSuffix(r.URL.Path, "/logs"):
					executed = true
					http.Error(w, "code must not start", 500)
				default:
					http.NotFound(w, r)
				}
			})
			if err := RunRemote(t.Context(), options); err == nil {
				t.Fatal("denied credentials returned success")
			}
			mu.Lock()
			defer mu.Unlock()
			if executed || result.State != tc.want || tc.want == run.Cancelled && result.Error != "" {
				t.Fatal(executed, result)
			}
		})
	}
}

func TestRemoteBatchesSmallMaskedUnicodeWrites(t *testing.T) {
	var mu sync.Mutex
	var logs []run.Log
	var result run.Result
	options := remoteTestOptions(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/claim"):
			plan := remoteTestPlan()
			plan.Steps[0].Run = "printf '::add-mask::private-key\\n'; i=0; while [ \"$i\" -lt 3000 ]; do printf '第%s行 🚀 private-key\\n' \"$i\"; i=$((i+1)); done"
			_ = json.NewEncoder(w).Encode(plan)
		case strings.HasSuffix(r.URL.Path, "/control"):
			_, _ = w.Write([]byte(`{"cancel_requested":false}`))
		case strings.HasSuffix(r.URL.Path, "/logs"):
			var entry run.Log
			_ = json.NewDecoder(r.Body).Decode(&entry)
			logs = append(logs, entry)
			_ = json.NewEncoder(w).Encode(map[string]int64{"seq": entry.Seq})
		case strings.HasSuffix(r.URL.Path, "/result"):
			_ = json.NewDecoder(r.Body).Decode(&result)
			_, _ = w.Write([]byte(`{"saved":true}`))
		default:
			http.NotFound(w, r)
		}
	})
	if err := RunRemote(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	var got, want strings.Builder
	for i, e := range logs {
		if e.Seq != int64(i+1) || e.StepID != "build" || len(e.Data) > 8<<10 || !utf8.ValidString(e.Data) {
			t.Fatalf("invalid chunk %d", i)
		}
		got.WriteString(e.Data)
	}
	for i := range 3000 {
		fmt.Fprintf(&want, "第%d行 🚀 ***\n", i)
	}
	if got.String() != want.String() || len(logs) > 32 || result.LastLogSeq != int64(len(logs)) {
		t.Fatalf("batch lost/changed logs: chunks=%d bytes=%d expected=%d seq=%d", len(logs), got.Len(), want.Len(), result.LastLogSeq)
	}
}

func TestRemoteNeverExtendsAnAttemptedSequenceAfterLostACK(t *testing.T) {
	var mu sync.Mutex
	var attempts []run.Log
	var result run.Result
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	message := strings.Repeat("hello-", 30) + "tail\n"
	options := remoteTestOptions(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/claim"):
			p := remoteTestPlan()
			p.SecretNames = []string{"TOKEN"}
			p.Steps[0].Run = "printf '%s' '" + message + "'; sleep 30"
			_ = json.NewEncoder(w).Encode(p)
		case strings.HasSuffix(r.URL.Path, "/credentials"):
			_, _ = w.Write([]byte(`{"secrets":{"TOKEN":"long-static-secret-0123456789"}}`))
		case strings.HasSuffix(r.URL.Path, "/control"):
			_, _ = w.Write([]byte(`{"cancel_requested":false}`))
		case strings.HasSuffix(r.URL.Path, "/logs"):
			var e run.Log
			_ = json.NewDecoder(r.Body).Decode(&e)
			attempts = append(attempts, e)
			if len(attempts) == 1 {
				cancel()
				http.Error(w, "ACK lost", http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]int64{"seq": e.Seq})
		case strings.HasSuffix(r.URL.Path, "/result"):
			_ = json.NewDecoder(r.Body).Decode(&result)
			_, _ = w.Write([]byte(`{"saved":true}`))
		default:
			http.NotFound(w, r)
		}
	})
	if RunRemote(ctx, options) == nil {
		t.Fatal("cancelled execution succeeded")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(attempts) != 3 || attempts[0] != attempts[1] || attempts[2].Seq != 2 || attempts[1].Data+attempts[2].Data != message || result.LastLogSeq != 2 || result.State != run.Cancelled {
		t.Fatalf("retry mutated or lost output: %+v result=%+v", attempts, result)
	}
}

func TestPeriodicLogFailureStopsCodeEvenWhenFinalFlushRecovers(t *testing.T) {
	var mu sync.Mutex
	var result run.Result
	calls := 0
	options := remoteTestOptions(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/claim"):
			p := remoteTestPlan()
			p.Steps[0].Run = "echo alive; sleep 30; touch should-not-exist"
			_ = json.NewEncoder(w).Encode(p)
		case strings.HasSuffix(r.URL.Path, "/control"):
			_, _ = w.Write([]byte(`{"cancel_requested":false}`))
		case strings.HasSuffix(r.URL.Path, "/logs"):
			var entry run.Log
			_ = json.NewDecoder(r.Body).Decode(&entry)
			calls++
			if calls == 1 {
				http.Error(w, "private-body", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]int64{"seq": entry.Seq})
		case strings.HasSuffix(r.URL.Path, "/result"):
			_ = json.NewDecoder(r.Body).Decode(&result)
			_, _ = w.Write([]byte(`{"saved":true}`))
		default:
			http.NotFound(w, r)
		}
	})
	if err := RunRemote(t.Context(), options); err == nil || strings.Contains(err.Error(), "private-body") {
		t.Fatal("log upload failure not propagated safely", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if result.State != run.Failed || result.LastLogSeq != 1 || calls != 2 {
		t.Fatalf("unsafe result %+v calls=%d", result, calls)
	}
	if _, err := os.Stat(filepath.Join(options.Workspace, "should-not-exist")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("execution outlived log failure")
	}
}

func TestJobTimeoutInterruptsPeriodicUploadBeforeBoundedFinalFlush(t *testing.T) {
	var mu sync.Mutex
	aborted := false
	logCalls := 0
	var result run.Result
	options := remoteTestOptions(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/claim"):
			p := remoteTestPlan()
			p.TimeoutSeconds = 1
			p.SecretNames = []string{"TOKEN"}
			p.Steps[0].Run = "printf 'enough text to leave a bounded secret tail at timeout\\n'; sleep 30"
			_ = json.NewEncoder(w).Encode(p)
		case strings.HasSuffix(r.URL.Path, "/credentials"):
			_, _ = w.Write([]byte(`{"secrets":{"TOKEN":"a-long-static-secret-0123456789"}}`))
		case strings.HasSuffix(r.URL.Path, "/control"):
			_, _ = w.Write([]byte(`{"cancel_requested":false}`))
		case strings.HasSuffix(r.URL.Path, "/logs"):
			var entry run.Log
			_ = json.NewDecoder(r.Body).Decode(&entry)
			mu.Lock()
			logCalls++
			first := logCalls == 1
			mu.Unlock()
			if first {
				<-r.Context().Done()
				mu.Lock()
				aborted = true
				mu.Unlock()
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]int64{"seq": entry.Seq})
		case strings.HasSuffix(r.URL.Path, "/result"):
			mu.Lock()
			_ = json.NewDecoder(r.Body).Decode(&result)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"saved":true}`))
		default:
			http.NotFound(w, r)
		}
	})
	options.RetryTimeout = 30 * time.Second
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	if RunRemote(ctx, options) == nil {
		t.Fatal("timed-out job succeeded")
	}
	mu.Lock()
	defer mu.Unlock()
	if result.State != run.TimedOut || result.LastLogSeq != 2 || !aborted {
		t.Fatalf("periodic upload blocked local timeout or lost its secret tail: %+v aborted=%v", result, aborted)
	}
}
