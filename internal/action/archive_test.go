package action

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func archiveHeader(t *testing.T, sha string, withPAX bool) []byte {
	t.Helper()
	var body bytes.Buffer
	gz := gzip.NewWriter(&body)
	writer := tar.NewWriter(gz)
	if withPAX {
		if err := writer.WriteHeader(&tar.Header{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": sha}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.WriteHeader(&tar.Header{Name: "repo/", Typeflag: tar.TypeDir, Mode: 0755}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}
func TestPublicArchiveRefResolutionAvoidsRESTAndPinsRawMetadata(t *testing.T) {
	for _, branch := range []bool{false, true} {
		t.Run(fmt.Sprint(branch), func(t *testing.T) {
			sha := strings.Repeat("a", 40)
			var calls []string
			archive := archiveHeader(t, sha, true)
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" {
					t.Error("source request received credentials")
				}
				calls = append(calls, r.URL.Path)
				if strings.Contains(r.URL.Path, "/tar.gz/") {
					if branch && strings.Contains(r.URL.Path, "/refs/tags/") {
						http.NotFound(w, r)
						return
					}
					_, _ = w.Write(archive)
					return
				}
				if !strings.Contains(r.URL.Path, "/"+sha+"/sub/action.yml") || r.URL.RawQuery != "" {
					t.Error("metadata not immutable", r.URL.String())
				}
				_, _ = fmt.Fprint(w, "runs: {using: node24, main: index.js}\n")
			}))
			defer source.Close()
			plans := refPlans("Owner/Repo/sub@release/v1")
			err := (Resolver{Client: source.Client(), ArchiveURL: source.URL, RawURL: source.URL}).Resolve(t.Context(), plans)
			if err != nil || plans[0].ActionPins["owner/repo@release/v1"] != sha {
				t.Fatal(plans, err)
			}
			want := 2
			if branch {
				want = 3
			}
			if len(calls) != want {
				t.Fatal(calls)
			}
		})
	}
}
func TestArchiveCommitHeaderFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{{"missing PAX", archiveHeader(t, strings.Repeat("a", 40), false)}, {"short SHA", archiveHeader(t, "abc", true)}, {"not gzip", []byte("not an archive")}, {"truncated", archiveHeader(t, strings.Repeat("a", 40), true)[:16]}} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := archiveCommitHeader(bytes.NewReader(tc.data)); err == nil {
				t.Fatal("accepted unverified archive commit")
			}
		})
	}
	sha := strings.Repeat("A", 40)
	if got, err := archiveCommitHeader(bytes.NewReader(archiveHeader(t, sha, true))); err != nil || got != strings.ToLower(sha) {
		t.Fatal(got, err)
	}
}

func TestArchiveHeaderCancellationKeepsEventRetryable(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		cancel()
		<-r.Context().Done()
	}))
	defer source.Close()
	err := (Resolver{Client: source.Client(), ArchiveURL: source.URL, RawURL: source.URL}).Resolve(ctx, refPlans("owner/repo@v1"))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatal("cancelled acquisition was marked permanently invalid", err)
	}
}

func TestArchiveSourceMissingAndUnavailableAreDistinguished(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }))
			defer source.Close()
			err := (Resolver{Client: source.Client(), ArchiveURL: source.URL, RawURL: source.URL}).Resolve(t.Context(), refPlans("owner/repo@v1"))
			if err == nil || errors.Is(err, ErrUnavailable) != (code != http.StatusNotFound) {
				t.Fatal(err)
			}
		})
	}
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(archiveHeader(t, "invalid", true)) }))
	defer source.Close()
	err := (Resolver{Client: source.Client(), ArchiveURL: source.URL, RawURL: source.URL}).Resolve(t.Context(), refPlans("owner/repo@v1"))
	if err == nil || errors.Is(err, ErrUnavailable) {
		t.Fatal("malformed source metadata must fail closed", err)
	}
}

func TestArchiveHeaderCancellationDoesNotReuseHTTP2Connection(t *testing.T) {
	sha := strings.Repeat("d", 40)
	archive := archiveHeader(t, sha, true)
	var mu sync.Mutex
	connections := map[string]bool{}
	source := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/tar.gz/") {
			if r.ProtoMajor != 2 {
				t.Error("probe did not use HTTP/2")
			}
			mu.Lock()
			connections[r.RemoteAddr] = true
			mu.Unlock()
			_, _ = w.Write(archive)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		_, _ = fmt.Fprint(w, "runs: {using: node24, main: index.js}\n")
	}))
	source.EnableHTTP2 = true
	source.StartTLS()
	defer source.Close()
	plans := refPlans("owner/repo@v1", "owner/repo@v2")
	if err := (Resolver{Client: source.Client(), ArchiveURL: source.URL, RawURL: source.URL}).Resolve(t.Context(), plans); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(connections) != 2 {
		t.Fatal("cancelled archive streams reused a pooled connection", connections)
	}
}
