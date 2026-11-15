package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

type pollProbeBackend struct{ backend }

func (pollProbeBackend) buildPlatforms(context.Context) string { return "none" }

func TestContainerCapablePollWorksWithLegacyRequestDecoder(t *testing.T) {
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Attempt string }
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil || body.Attempt != "attempt" {
			http.Error(w, "legacy request decoder rejected the poll", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer legacy.Close()
	base, err := url.Parse(legacy.URL)
	if err != nil {
		t.Fatal(err)
	}
	c := client{url: base, http: *legacy.Client(), backend: pollProbeBackend{}, docker: &dockerExecutor{}}
	code, err := c.send(t.Context(), "/poll", request{Attempt: "attempt"}, nil)
	if err != nil || code != http.StatusNoContent {
		t.Fatalf("legacy poll = %d, %v", code, err)
	}
}

func TestRunningBuildPlatforms(t *testing.T) {
	for _, tc := range []struct{ name, output, want string }{
		{"running", "Status: running\nPlatforms: linux/arm64, linux/amd64*, linux/386\n", "linux/amd64,linux/arm64"},
		{"stopped", "Status: stopped\nPlatforms: linux/amd64*\n", "none"},
		{"multiple nodes", "Status: stopped\nPlatforms: linux/arm64\nStatus: running\nPlatforms: linux/amd64,linux/amd64/v2\n", "linux/amd64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := runningBuildPlatforms(tc.output); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
