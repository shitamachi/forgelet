package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type observedTransferTransport struct {
	base      http.RoundTripper
	deadlines chan time.Duration
}

func (o observedTransferTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	deadline, ok := r.Context().Deadline()
	if ok {
		o.deadlines <- time.Until(deadline)
	} else {
		o.deadlines <- 0
	}
	return o.base.RoundTrip(r)
}

func TestBlobTransferDeadlinesKeepParentBound(t *testing.T) {
	for _, tc := range []struct {
		name, kind   string
		parent, want time.Duration
	}{
		{"cache", "cache", 0, 5 * time.Minute},
		{"artifact", "artifact", 0, 2 * time.Minute},
		{"shorter parent", "cache", 2 * time.Second, 2 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := remoteTestOptions(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				_, _ = io.WriteString(w, `{}`)
			})
			observed := make(chan time.Duration, 1)
			opts.Client.Transport = observedTransferTransport{base: opts.Client.Transport, deadlines: observed}
			client, err := newRemoteClient(opts)
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(t.TempDir(), "archive")
			if err := os.WriteFile(file, []byte("payload"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
			defer cancel()
			if tc.parent != 0 {
				ctx, cancel = context.WithTimeout(ctx, tc.parent)
				defer cancel()
			}
			if _, err := client.Upload(ctx, tc.kind, "key", "version", 7, file); err != nil {
				t.Fatal(err)
			}
			remaining := <-observed
			if remaining < tc.want-time.Second || remaining > tc.want {
				t.Fatalf("transfer deadline = %s, want near %s", remaining, tc.want)
			}
		})
	}
}

func TestCacheTransferStillCancelsWhileAwaitingResponse(t *testing.T) {
	started := make(chan struct{})
	opts := remoteTestOptions(t, func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	})
	client, err := newRemoteClient(opts)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "archive")
	if err := os.WriteFile(file, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := client.Upload(ctx, "cache", "key", "version", 7, file); done <- err }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("cache transfer did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel result: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cache transfer ignored cancellation")
	}
}
