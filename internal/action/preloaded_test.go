package action

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shitamachi/forgelet/internal/run"
)

func preloadFixture(t *testing.T, root, sha string, corrupt, unsafe bool) {
	t.Helper()
	var data bytes.Buffer
	gz := gzip.NewWriter(&data)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": sha}}); err != nil {
		t.Fatal(err)
	}
	name := "repo/action.yml"
	if unsafe {
		name = "repo/../escape"
	}
	payload := []byte("runs: {using: node20, main: index.js}\n")
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0755, Typeflag: tar.TypeReg, Size: int64(len(payload))}); err != nil {
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
	folder := filepath.Join(root, "owner/repo")
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, sha+".tar.gz"), data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(data.Bytes()))
	if corrupt {
		hash = strings.Repeat("0", 64)
	}
	manifest, err := json.Marshal(map[string]string{"owner/repo@" + sha: hash})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadPreloadedIntegrityAndBoundaries(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for _, tc := range []struct {
		name           string
		prepare        func(*testing.T, string)
		ref            string
		found, wantErr bool
	}{
		{"verified", func(t *testing.T, p string) { preloadFixture(t, p, sha, false, false) }, sha, true, false},
		{"missing manifest", func(*testing.T, string) {}, sha, false, false},
		{"other commit", func(t *testing.T, p string) { preloadFixture(t, p, sha, false, false) }, strings.Repeat("b", 40), false, false},
		{"checksum mismatch", func(t *testing.T, p string) { preloadFixture(t, p, sha, true, false) }, sha, true, true},
		{"unsafe archive", func(t *testing.T, p string) { preloadFixture(t, p, sha, false, true) }, sha, true, true},
		{"bad manifest", func(t *testing.T, p string) {
			if err := os.WriteFile(filepath.Join(p, "manifest.json"), []byte("not json"), 0600); err != nil {
				t.Fatal(err)
			}
		}, sha, false, true},
		{"mutable reference", func(*testing.T, string) {}, "main", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.prepare(t, root)
			dest := t.TempDir()
			found, err := LoadPreloaded(t.Context(), root, run.Action{Repository: "owner/repo", SHA: tc.ref}, dest)
			if found != tc.found || (err != nil) != tc.wantErr {
				t.Fatalf("found=%t error=%v", found, err)
			}
			if tc.name == "verified" {
				if _, err := os.Stat(filepath.Join(dest, "action.yml")); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := LoadPreloaded(ctx, t.TempDir(), run.Action{Repository: "owner/repo", SHA: sha}, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// Cross-compiled and executed in the actual bundle image by the optional
// Testcontainers fixture. Network is disabled; every advertised bundle is read.
func TestPreloadedImageArchives(t *testing.T) {
	root := os.Getenv("FORGELET_TEST_IMAGE_ACTION_ROOT")
	if root == "" {
		t.Skip("set FORGELET_TEST_IMAGE_ACTION_ROOT inside the actual bundled image")
	}
	raw, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var entries map[string]string
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("empty image bundle")
	}
	for key := range entries {
		t.Run(key, func(t *testing.T) {
			a, err := RemoteReference(key)
			if err != nil {
				t.Fatal(err)
			}
			found, err := LoadPreloaded(t.Context(), root, *a, t.TempDir())
			if err != nil || !found {
				t.Fatal(found, err)
			}
		})
	}
}

func TestLoadPreloadedRejectsBrokenImageLocks(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for _, name := range []string{"missing archive", "wrong commit", "invalid hash", "oversized archive", "oversized manifest", "too many entries"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			preloadFixture(t, root, sha, false, false)
			archive := filepath.Join(root, "owner/repo", sha+".tar.gz")
			manifest := filepath.Join(root, "manifest.json")
			switch name {
			case "missing archive":
				if err := os.Remove(archive); err != nil {
					t.Fatal(err)
				}
			case "wrong commit":
				other := strings.Repeat("b", 40)
				preloadFixture(t, root, other, false, false)
				if err := os.Rename(filepath.Join(root, "owner/repo", other+".tar.gz"), archive); err != nil {
					t.Fatal(err)
				}
				b, err := os.ReadFile(archive)
				if err != nil {
					t.Fatal(err)
				}
				lock, err := json.Marshal(map[string]string{"owner/repo@" + sha: fmt.Sprintf("%x", sha256.Sum256(b))})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(manifest, lock, 0600); err != nil {
					t.Fatal(err)
				}
			case "invalid hash":
				if err := os.WriteFile(manifest, []byte(`{"owner/repo@`+sha+`":"invalid"}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "oversized archive":
				if err := os.Truncate(archive, (32<<20)+1); err != nil {
					t.Fatal(err)
				}
			case "oversized manifest":
				if err := os.WriteFile(manifest, []byte(strings.Repeat(" ", (64<<10)+1)), 0600); err != nil {
					t.Fatal(err)
				}
			case "too many entries":
				entries := map[string]string{}
				for i := range 129 {
					entries[fmt.Sprint(i)] = strings.Repeat("0", 64)
				}
				b, err := json.Marshal(entries)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(manifest, b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			dest := t.TempDir()
			if _, err := LoadPreloaded(t.Context(), root, run.Action{Repository: "owner/repo", SHA: sha}, dest); err == nil {
				t.Fatal("broken image lock accepted")
			}
			entries, err := os.ReadDir(dest)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatal("unverified files extracted")
			}
		})
	}
}
