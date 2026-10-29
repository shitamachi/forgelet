package actworker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestPreloadedActionUsesExactCommitWithoutNetwork(t *testing.T) {
	sha := strings.Repeat("a", 40)
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": sha}}); err != nil {
		t.Fatal(err)
	}
	payload := []byte("runs: {using: node20, main: index.js}\n")
	if err := tw.WriteHeader(&tar.Header{Name: "repo/action.yml", Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(payload))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	folder := filepath.Join(root, "owner/repo")
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, sha+".tar.gz"), archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, "public source unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(corrupt), func(t *testing.T) {
			checksum := fmt.Sprintf("%x", sha256.Sum256(archive.Bytes()))
			if corrupt {
				checksum = strings.Repeat("0", 64)
			}
			data, err := json.Marshal(map[string]string{"owner/repo@" + sha: checksum})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "manifest.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			cache := t.TempDir()
			s := sources{dir: cache, base: server.URL, preloaded: root, pins: map[string]string{"owner/repo@stable": sha}}
			_, err = s.Fetch(t.Context(), "owner/repo", "https://github.com/owner/repo", "stable", "")
			if (err != nil) != corrupt {
				t.Fatal(err)
			}
			if !corrupt {
				body, err := s.GetTarArchive(t.Context(), "owner/repo", sha, ".")
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(body)
				if err != nil {
					t.Fatal(err)
				}
				if err := body.Close(); err != nil {
					t.Fatal(err)
				}
				if !bytes.Contains(data, payload) {
					t.Fatal("preloaded Action body lost")
				}
			}
			entries, err := os.ReadDir(filepath.Join(cache, "owner/repo"))
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".") {
					t.Fatal("temporary source directory leaked")
				}
			}
		})
	}
	if requests != 0 {
		t.Fatalf("preloaded success/corruption fell back to network: %d", requests)
	}
}
