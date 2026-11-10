package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPairingCollectsCredentialOnce(t *testing.T) {
	token := "frr_" + strings.Repeat("a", 64)
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/runner/v1/pair":
			var input map[string]string
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input["hostname"] == "" || input["os"] == "" {
				http.Error(w, "invalid", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "pairing-1", "code": "ABCD-EFGH", "secret": strings.Repeat("00", 32),
				"expires_at": time.Now().Add(time.Minute),
			})
		case r.Method == http.MethodGet && r.URL.Path == "/runner/v1/pair/pairing-1":
			if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("00", 32) {
				http.Error(w, "bad secret", http.StatusNotFound)
				return
			}
			if polls.Add(1) < 2 {
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(`{"state":"pending"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"state": "approved", "name": "lab-box", "token": token})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	workRoot := t.TempDir()
	var codes []string
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	paired, err := pair(ctx, server.URL, workRoot, func(status Status) {
		if status.Code != "" && (len(codes) == 0 || codes[len(codes)-1] != status.Code) {
			codes = append(codes, status.Code)
		}
	})
	if err != nil || paired != token {
		t.Fatalf("pair = %q, %v", paired, err)
	}
	if len(codes) == 0 || codes[0] != "ABCD-EFGH" {
		t.Fatalf("pairing code not reported: %v", codes)
	}
	if got := readCredential(workRoot); got != token {
		t.Fatalf("stored credential = %q", got)
	}
	if !validToken("frr_" + strings.Repeat("a", 64)) {
		t.Fatal("issued token rejected")
	}
	for _, candidate := range []string{"frr_zz", "nope", ""} {
		if validToken(candidate) {
			t.Fatalf("%q accepted", candidate)
		}
	}
}

func TestPairingAsksForANewCodeWhenItExpires(t *testing.T) {
	var issued atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			count := issued.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": fmt.Sprintf("pairing-%d", count), "code": fmt.Sprintf("CODE-000%d", count),
				"secret": strings.Repeat("11", 32), "expires_at": time.Now().Add(time.Minute),
			})
			return
		}
		// Every code expires immediately.
		http.NotFound(w, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	_, err := pair(ctx, server.URL, t.TempDir(), func(Status) {})
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("pair returned %v, want the context error", err)
	}
	if issued.Load() < 2 {
		t.Fatalf("no fresh code was requested: %d", issued.Load())
	}
}

func TestCredentialFileIsValidated(t *testing.T) {
	root := t.TempDir()
	if err := writeCredential(root, "frr_"+strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(root, credentialFile))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("credential mode %v", info.Mode())
		}
	}
	if err := os.WriteFile(filepath.Join(root, credentialFile), []byte("garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readCredential(root); got != "" {
		t.Fatalf("invalid credential accepted: %q", got)
	}
}
