package action

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReferencesAreImmutable(t *testing.T) {
	a, err := Reference("actions/setup-go@v6.5.0")
	if err != nil || a.SHA != SetupGoSHA {
		t.Fatal(a, err)
	}
	for _, input := range []string{"owner/repo@main", "owner/repo@v1", "owner/../repo@" + SetupGoSHA, "./../escape", "docker://alpine", "./a/../../escape"} {
		if _, err := Reference(input); err == nil {
			t.Error("accepted", input)
		}
	}
	a, err = Reference("owner/repo/path@" + SetupGoSHA)
	if err != nil || a.Path != "path" {
		t.Fatal(a, err)
	}
}
func TestDownloadArchiveBoundaries(t *testing.T) {
	for _, kind := range []string{"valid", "pax", "root directory", "escape", "symlink", "absolute", "duplicate", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			var body bytes.Buffer
			gz := gzip.NewWriter(&body)
			tw := tar.NewWriter(gz)
			if kind == "pax" {
				if err := tw.WriteHeader(&tar.Header{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": SetupGoSHA}}); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "root directory" {
				if err := tw.WriteHeader(&tar.Header{Name: "repo", Typeflag: tar.TypeDir, Mode: 0700}); err != nil {
					t.Fatal(err)
				}
			}
			h := &tar.Header{Name: "repo/action.yml", Mode: 0600, Size: 3}
			switch kind {
			case "escape":
				h.Name = "repo/../escape"
			case "absolute":
				h.Name = "repo//absolute"
			case "symlink":
				h.Typeflag = tar.TypeSymlink
				h.Linkname = "/tmp/outside"
				h.Size = 0
			case "oversized":
				h.Size = 129 << 20
			}
			if err := tw.WriteHeader(h); err != nil {
				t.Fatal(err)
			}
			if h.Size == 3 {
				_, _ = tw.Write([]byte("abc"))
			}
			if kind == "duplicate" {
				_ = tw.WriteHeader(h)
				_, _ = tw.Write([]byte("abc"))
			}
			_ = tw.Close()
			_ = gz.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, SetupGoSHA) {
					t.Error("not pinned")
				}
				_, _ = w.Write(body.Bytes())
			}))
			defer server.Close()
			dir := t.TempDir()
			a, _ := Reference("owner/repo@" + SetupGoSHA)
			err := Download(t.Context(), server.Client(), server.URL, *a, dir)
			if kind == "valid" || kind == "pax" || kind == "root directory" {
				if err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(filepath.Join(dir, "action.yml"))
				if err != nil || string(data) != "abc" {
					t.Fatal(string(data), err)
				}
			} else if err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
}
