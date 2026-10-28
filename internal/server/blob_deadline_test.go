package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/blob"
)

type blobDeadlineResponse struct {
	*httptest.ResponseRecorder
	deadline time.Time
}

func (w *blobDeadlineResponse) SetReadDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}

func TestRuntimeBlobDeadlineRequiresWorkloadAuthentication(t *testing.T) {
	h := newHarness(t, 1)
	h.http.Close()
	var err error
	h.cfg.Blobs, err = blob.Open(filepath.Join(t.TempDir(), "blobs"), blob.Limits{ObjectBytes: 1024, CacheObjectBytes: 2 << 30, TotalBytes: 4 << 30, Objects: 10})
	if err != nil {
		t.Fatal(err)
	}
	h.start()
	item := h.manual(testWorkflow)
	h.tick()
	job, err := h.store.GetJob(t.Context(), item.Jobs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if status, body := h.runtimeRequest("POST", "claim", job, nil); status != 200 {
		t.Fatal(status, string(body))
	}
	for _, tc := range []struct {
		kind    string
		auth    bool
		status  int
		timeout time.Duration
	}{
		{"cache", true, 200, 5 * time.Minute},
		{"artifact", true, 200, 2 * time.Minute},
		{"cache", false, 401, 0},
	} {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/internal/jobs/"+job.ID+"/blobs/"+tc.kind+"?key=deadline&version="+strings.Repeat("a", 64), strings.NewReader("payload"))
		if tc.auth {
			r.Header.Set("Authorization", "Bearer token-"+job.ID)
		}
		w := &blobDeadlineResponse{ResponseRecorder: httptest.NewRecorder()}
		h.server.Handler().ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s auth=%t: %d %s", tc.kind, tc.auth, w.Code, w.Body)
		}
		if tc.timeout == 0 {
			if !w.deadline.IsZero() {
				t.Fatal("extended an unauthenticated transfer")
			}
			continue
		}
		remaining := time.Until(w.deadline)
		if remaining < tc.timeout-time.Second || remaining > tc.timeout {
			t.Fatalf("%s read deadline = %s", tc.kind, remaining)
		}
	}
}
