package actworker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestActionArchivesAcceptDefaultRootAndBoundSubpaths(t *testing.T) {
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	writer := tar.NewWriter(gz)
	for _, name := range []string{"action.yml", "nested/action.yml"} {
		data := []byte("name: fixture\nruns:\n  using: node20\n  main: main.js\n")
		if err := writer.WriteHeader(&tar.Header{Name: "repo-root/" + name, Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			http.Error(w, "temporary upstream failure", http.StatusServiceUnavailable)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/"+strings.Repeat("a", 40)) {
			t.Error(r.URL.Path)
		}
		_, _ = w.Write(archive.Bytes())
	}))
	defer server.Close()
	source := sources{dir: t.TempDir(), base: server.URL, pins: map[string]string{"owner/repo@stable": strings.Repeat("a", 40)}}
	if _, err := source.Fetch(t.Context(), "owner/repo", "https://github.com/owner/repo", "stable", ""); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"", ".", "nested", "action.yml", "nested/action.yml"} {
		data, err := source.GetTarArchive(t.Context(), "owner/repo", strings.Repeat("a", 40), prefix)
		if err != nil {
			t.Fatal(prefix, err)
		}
		reader := tar.NewReader(data)
		first, err := reader.Next()
		if err != nil || first.Name != "action.yml" {
			t.Fatal(first, err)
		}
		_, err = io.Copy(io.Discard, reader)
		if err != nil {
			t.Fatal(err)
		}
		if err := data.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if requests != 2 {
		t.Fatal("did not recover and reuse published source", requests)
	}
	for _, prefix := range []string{"..", "../other", "/absolute"} {
		if _, err := source.GetTarArchive(t.Context(), "owner/repo", strings.Repeat("a", 40), prefix); err == nil {
			t.Fatal("accepted path escape", prefix)
		}
	}
}

func TestActionSourceUsesSavedTransitiveLockAndRejectsMissingTags(t *testing.T) {
	source := sources{dir: t.TempDir(), pins: map[string]string{"child/repo@stable": strings.Repeat("c", 40)}}
	// Fetch is allowed to reach ensure only for a fixed source. A missing lock
	// must fail before I/O, including aliases that legacy plans used to accept.
	for _, uses := range []struct{ key, ref string }{{"owner/repo", "main"}, {"actions/setup-go", "v5"}} {
		if _, err := source.Fetch(t.Context(), uses.key, "https://github.com/"+uses.key, uses.ref, "must-not-leak"); err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatal(err)
		}
	}
}
