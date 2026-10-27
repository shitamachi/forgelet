//go:build integration

package integration_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
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
