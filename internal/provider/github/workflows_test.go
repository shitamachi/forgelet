package github

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestWorkflowsUsesFixedSHAAndConfiguredOrigin(t *testing.T) {
	var requested []string
	client, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/access_tokens") {
			writeToken(t, w)
			return
		}
		if r.Header.Get("Authorization") != "Bearer installation-secret" || r.URL.Query().Get("ref") != testSHA {
			t.Errorf("invalid auth/ref: %s", r.URL)
		}
		requested = append(requested, r.URL.Path)
		switch r.URL.Path {
		case "/repos/owner/repo/contents/.github/workflows":
			writeJSON(t, w, []map[string]any{
				{"name": "ci.yml", "path": ".github/workflows/ci.yml", "type": "file", "size": 10, "download_url": "http://attacker.internal"},
				{"name": "test.yaml", "path": ".github/workflows/test.yaml", "type": "file", "size": 10},
				{"name": "README.md", "path": ".github/workflows/README.md", "type": "file"},
			})
		case "/repos/owner/repo/contents/.github/workflows/ci.yml", "/repos/owner/repo/contents/.github/workflows/test.yaml":
			writeJSON(t, w, contentEntry{Path: strings.TrimPrefix(r.URL.Path, "/repos/owner/repo/contents/"), Type: "file", Encoding: "base64", Content: base64.StdEncoding.EncodeToString([]byte("on: push\n")), Size: 9})
		default:
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
		}
	})
	event := run.Event{Repository: "owner/repo", SHA: testSHA, InstallationID: 7, Ref: "refs/heads/main"}
	files, err := client.Workflows(t.Context(), event)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || string(files[".github/workflows/ci.yml"]) != "on: push\n" || len(requested) != 3 {
		t.Fatalf("files=%v requests=%v", files, requested)
	}
}

func TestWorkflowLimitsAndMalformedEntries(t *testing.T) {
	entry := contentEntry{Name: "ci.yml", Path: ".github/workflows/ci.yml", Type: "file", Size: 9}
	file := contentEntry{Path: entry.Path, Type: "file", Encoding: "base64", Content: base64.StdEncoding.EncodeToString([]byte("on: push\n")), Size: 9}
	for _, tc := range []struct {
		name      string
		entries   []contentEntry
		file      contentEntry
		status    int
		wantEmpty bool
	}{
		{"missing directory", nil, file, http.StatusNotFound, true},
		{"directory denied", nil, file, http.StatusForbidden, false},
		{"symlink", []contentEntry{{Name: "ci.yml", Path: entry.Path, Type: "symlink"}}, file, 0, false},
		{"path escape", []contentEntry{{Name: "../ci.yml", Path: ".github/workflows/../ci.yml", Type: "file"}}, file, 0, false},
		{"mismatched path", []contentEntry{{Name: "ci.yml", Path: "elsewhere/ci.yml", Type: "file"}}, file, 0, false},
		{"oversized entry", []contentEntry{{Name: entry.Name, Path: entry.Path, Type: "file", Size: maxWorkflowBytes + 1}}, file, 0, false},
		{"duplicate entry", []contentEntry{entry, entry}, file, 0, false},
		{"unsupported encoding", []contentEntry{entry}, contentEntry{Path: entry.Path, Type: "file", Encoding: "none"}, 0, false},
		{"invalid base64", []contentEntry{entry}, contentEntry{Path: entry.Path, Type: "file", Encoding: "base64", Content: "!invalid"}, 0, false},
		{"decoded size limit", []contentEntry{entry}, contentEntry{Path: entry.Path, Type: "file", Encoding: "base64", Content: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", maxWorkflowBytes+1)))}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/access_tokens") {
					writeToken(t, w)
					return
				}
				if strings.HasSuffix(r.URL.Path, "/workflows") {
					if tc.status != 0 {
						w.WriteHeader(tc.status)
						return
					}
					writeJSON(t, w, tc.entries)
					return
				}
				writeJSON(t, w, tc.file)
			})
			files, err := client.Workflows(t.Context(), run.Event{Repository: "owner/repo", SHA: testSHA, InstallationID: 7})
			if tc.wantEmpty {
				if err != nil || len(files) != 0 {
					t.Fatalf("files=%v error=%v", files, err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestWorkflowEventValidation(t *testing.T) {
	client, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid event reached HTTP") })
	for _, event := range []run.Event{{Repository: "../repo", SHA: testSHA, InstallationID: 7}, {Repository: "owner/repo", SHA: "main", InstallationID: 7}} {
		if _, err := client.Workflows(t.Context(), event); err == nil {
			t.Fatal("invalid event accepted")
		}
	}
}
