//go:build integration

package integration_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFluxManifestDownload(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		wantCalls int
		wantError bool
	}{
		{"transient then successful", 503, "unavailable", 3, false},
		{"permanent denied", 403, "denied", 1, true},
		{"bounded manifest", 200, strings.Repeat("x", (4<<20)+1), 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				if tc.status == 503 && calls == 3 {
					_, _ = fmt.Fprint(w, "kind: Namespace\n")
					return
				}
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			data, err := downloadFluxManifest(t.Context(), server.Client(), server.URL)
			if (err != nil) != tc.wantError || calls != tc.wantCalls {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if !tc.wantError && string(data) != "kind: Namespace\n" {
				t.Fatalf("unexpected manifest: %q", data)
			}
		})
	}
}

func TestCachedFluxManifestRejectsChangedOrOversizedInput(t *testing.T) {
	for _, tc := range []struct {
		name    string
		data    []byte
		message string
	}{
		{"changed", []byte("untrusted manifest"), "checksum differs"},
		{"oversized", make([]byte, (4<<20)+1), "exceeds 4 MiB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "install.yaml")
			if err := os.WriteFile(file, tc.data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := cachedFluxManifest(file); err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatal(err)
			}
		})
	}
}
