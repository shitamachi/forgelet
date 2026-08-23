package github

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodePull(t *testing.T) {
	body := []byte(`{
  "action": "synchronize",
  "number": 7,
  "pull_request": {
    "head": {"ref": "feature", "sha": "head123", "repo": {"fork": true, "name": "fork-repo", "owner": {"login": "contributor"}}},
    "base": {"ref": "main", "repo": {"name": "forgelet", "owner": {"login": "shitamachi"}}}
  }
}`)
	ev, info, err := DecodePull(body, "pr-1")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ev.Name != "pull_request" || ev.SHA != "head123" || ev.Ref != "refs/heads/feature" {
		t.Errorf("event = %+v", ev)
	}
	if ev.Repository.Owner != "contributor" || ev.Repository.Name != "fork-repo" {
		t.Errorf("repo should point at head: %+v", ev.Repository)
	}
	if !info.Fork || info.BaseRef != "main" {
		t.Errorf("info = %+v", info)
	}

	closed := strings.Replace(string(body), `"synchronize"`, `"closed"`, 1)
	if _, _, err := DecodePull([]byte(closed), "pr-2"); !errors.Is(err, ErrIgnoredPush) {
		t.Errorf("closed action: %v", err)
	}
	sameRepo := strings.Replace(string(body), `"fork": true`, `"fork": false`, 1)
	_, info2, err := DecodePull([]byte(sameRepo), "pr-3")
	if err != nil || info2.Fork {
		t.Errorf("same-repo PR misclassified: %+v %v", info2, err)
	}
	if _, _, err := DecodePull([]byte("{}"), "pr-4"); !errors.Is(err, ErrMalformedPayload) {
		t.Errorf("empty payload: %v", err)
	}
}

func TestContentClientWorkflowsDir(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer ghs_x" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/repos/o/r") && r.URL.RawQuery == "":
			_, _ = w.Write([]byte(`{"default_branch":"trunk"}`))
		case strings.HasSuffix(r.URL.Path, "/repos/o/r/contents/.github/workflows"):
			if r.URL.Query().Get("ref") != "abc" {
				http.Error(w, "bad ref", http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`[
				{"name":"ci.yml","path":".github/workflows/ci.yml","type":"file"},
				{"name":"notes.txt","path":".github/workflows/notes.txt","type":"file"},
				{"name":"sub","path":".github/workflows/sub","type":"dir"}
			]`))
		case strings.HasSuffix(r.URL.Path, "/repos/o/r/contents/.github/workflows/ci.yml"):
			content := base64.StdEncoding.EncodeToString([]byte("on: push\njobs:\n  a:\n    runs-on: x\n    steps:\n      - run: x\n"))
			_, _ = w.Write([]byte(`{"content":"` + content + `","encoding":"base64"}`))
		default:
			http.Error(w, "not found "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := NewContentClient(srv.URL, srv.Client(), staticToken{})

	branch, err := c.DefaultBranch(context.Background(), "o", "r")
	if err != nil || branch != "trunk" {
		t.Fatalf("default branch: %q %v", branch, err)
	}
	files, err := c.WorkflowsDir(context.Background(), "o", "r", "abc")
	if err != nil {
		t.Fatalf("workflows: %v", err)
	}
	if len(files) != 1 || files[0].Name != "ci.yml" {
		t.Fatalf("files = %+v", files)
	}
	if !strings.Contains(string(files[0].Data), "on: push") {
		t.Errorf("content = %q", files[0].Data)
	}
}

func TestContentClientErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/contents/.github/workflows") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()
	c := NewContentClient(srv.URL, srv.Client(), staticToken{})
	if _, err := c.WorkflowsDir(context.Background(), "o", "r", "x"); err == nil || !strings.Contains(err.Error(), "status 403") {
		t.Errorf("list error: %v", err)
	}
	if _, err := c.DefaultBranch(context.Background(), "o", "r"); err == nil {
		t.Error("missing default branch must fail")
	}
}
