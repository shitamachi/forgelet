package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/blob"
	gh "github.com/shitamachi/forgelet/internal/provider/github"
	"github.com/shitamachi/forgelet/internal/run"
)

func TestCacheReadRefs(t *testing.T) {
	s := &Server{}
	s.authorization.Store(&GitHubAuthorization{Catalog: &gh.Catalog{Repositories: []gh.Repository{{Name: "owner/repo", DefaultBranch: "trunk"}}}})
	for _, tc := range []struct {
		name, repo, ref, event, base string
		want                         []string
	}{
		{"branch", "owner/repo", "refs/heads/feature", "push", "", []string{"refs/heads/feature", "refs/heads/trunk"}},
		{"default deduplicated", "owner/repo", "refs/heads/trunk", "push", "", []string{"refs/heads/trunk"}},
		{"PR base before default", "owner/repo", "refs/pull/12/merge", "pull_request", "release/next", []string{"refs/pull/12/merge", "refs/heads/release/next", "refs/heads/trunk"}},
		{"PR default base", "owner/repo", "refs/pull/12/merge", "pull_request", "trunk", []string{"refs/pull/12/merge", "refs/heads/trunk"}},
		{"push cannot choose base", "owner/repo", "refs/heads/topic", "push", "private", []string{"refs/heads/topic", "refs/heads/trunk"}},
		{"unknown repo", "other/repo", "refs/heads/topic", "push", "", []string{"refs/heads/topic"}},
		{"invalid base", "owner/repo", "refs/pull/12/merge", "pull_request", "refs/heads/private", []string{"refs/pull/12/merge", "refs/heads/trunk"}},
		{"tag", "owner/repo", "refs/tags/v1", "push", "", []string{"refs/tags/v1", "refs/heads/trunk"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := s.cacheReadRefs(run.Plan{Repository: tc.repo, Ref: tc.ref, EventName: tc.event, BaseRef: tc.base})
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
	s.authorization.Store(nil)
	if got := s.cacheReadRefs(run.Plan{Ref: "refs/heads/topic"}); len(got) != 1 {
		t.Fatal(got)
	}
}

func TestCacheFallbackHTTPIsolationAndWriteScope(t *testing.T) {
	h := newHarness(t, 4)
	h.http.Close()
	var err error
	blobDir := filepath.Join(t.TempDir(), "blobs")
	h.cfg.Blobs, err = blob.Open(blobDir, blob.Limits{ObjectBytes: 1024, TotalBytes: 65536, Objects: 64})
	if err != nil {
		t.Fatal(err)
	}
	h.start()
	h.server.authorization.Store(&GitHubAuthorization{Catalog: &gh.Catalog{Repositories: []gh.Repository{{Name: "me/repo", DefaultBranch: "trunk"}}}})
	create := func(ref string) run.Job {
		code, body := h.admin("POST", "/api/runs", map[string]any{"repository": "me/repo", "sha": testSHA, "ref": ref, "workflow": testWorkflow, "installation_id": 42})
		if code != 201 {
			t.Fatal(code, string(body))
		}
		var item run.Run
		if err := json.Unmarshal(body, &item); err != nil {
			t.Fatal(err)
		}
		h.tick()
		job, err := h.store.GetJob(t.Context(), item.Jobs[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		if code, body := h.runtimeRequest("POST", "claim", job, nil); code != 200 {
			t.Fatal(code, string(body))
		}
		return job
	}
	topic, main := create("refs/heads/topic"), create("refs/heads/trunk")
	version := strings.Repeat("a", 64)
	seed := func(repo, ref, key, value, v string) {
		_, err := h.cfg.Blobs.Put(t.Context(), blob.Scope{Repository: repo, Kind: "cache", Namespace: ref, Version: v}, key, "fixture", time.Hour, time.Now(), strings.NewReader(value))
		if err != nil {
			t.Fatal(err)
		}
	}
	seed("me/repo", "refs/heads/trunk", "dependency-exact", "default", version)
	seed("me/repo", "refs/heads/trunk", "dependency-other", "default-prefix", version)
	seed("me/repo", "refs/heads/topic", "local-old", "local-prefix", version)
	seed("other/repo", "refs/heads/trunk", "foreign", "foreign", version)
	seed("me/repo", "refs/heads/sibling", "sibling", "sibling", version)
	seed("me/repo", "refs/heads/trunk", "wrong-version", "wrong", strings.Repeat("b", 64))
	for _, tc := range []struct {
		job   run.Job
		query string
		code  int
		want  string
	}{
		{topic, "key=dependency-exact", 200, "default"},
		{topic, "key=missing&restore-key=dependency-", 200, "default-prefix"},
		{topic, "key=dependency-exact&restore-key=local-", 200, "local-prefix"},
		{topic, "key=foreign", 404, ""}, {topic, "key=sibling&namespace=refs/heads/sibling", 404, ""},
		{topic, "key=wrong-version", 404, ""}, {main, "key=local-old", 404, ""},
	} {
		code, body := h.runtimeRequest("GET", "blobs/cache?version="+version+"&"+tc.query, tc.job, nil)
		if code != tc.code || tc.want != "" && string(body) != tc.want {
			t.Fatalf("%s: %d %s", tc.query, code, body)
		}
	}
	// PR base comes from a persisted control-plane plan, never from the cache client.
	pr, err := h.server.createRunFiles(t.Context(), run.Event{Name: "pull_request", Action: "synchronize", Repository: "me/repo", SHA: testSHA, Ref: "refs/pull/7/merge", BaseRef: "release/next", InstallationID: 42, DeliveryID: "pr-cache-test"}, ".github/workflows/ci.yml", []byte(strings.Replace(testWorkflow, "on: push", "on: [push, pull_request]", 1)), nil)
	if err != nil {
		t.Fatal(err)
	}
	h.tick()
	prJob, err := h.store.GetJob(t.Context(), pr.Jobs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if code, body := h.runtimeRequest("POST", "claim", prJob, nil); code != 200 {
		t.Fatal(code, string(body))
	}
	seed("me/repo", "refs/heads/release/next", "dependency-exact", "base", version)
	if code, body := h.runtimeRequest("GET", "blobs/cache?version="+version+"&key=dependency-exact", prJob, nil); code != 200 || string(body) != "base" {
		t.Fatal(code, string(body))
	}
	// An unreadable current-ref object is an error, not a fallback miss.
	seed("me/repo", "refs/heads/topic", "broken", "unreadable", version)
	seed("me/repo", "refs/heads/trunk", "broken", "default", version)
	e, f, err := h.cfg.Blobs.Get(blob.Scope{Repository: "me/repo", Kind: "cache", Namespace: "refs/heads/topic", Version: version}, "broken", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	// The blob store uses a separate temporary directory from DataDir.
	brokenPath := filepath.Join(blobDir, e.ID, "data")
	if err = os.Remove(brokenPath); err != nil {
		t.Fatal(err)
	}
	if code, _ := h.runtimeRequest("GET", "blobs/cache?version="+version+"&key=broken", topic, nil); code != 500 {
		t.Fatal("hid corrupt current-ref object", code)
	}

	code, body := h.runtimeRequest("GET", "blobs/cache?lookup=true&version="+version+"&key=dependency-exact", topic, nil)
	var entry blob.Entry
	if code != 200 || json.Unmarshal(body, &entry) != nil || entry.Namespace != "refs/heads/trunk" {
		t.Fatal(code, string(body))
	}
	code, body = h.request("PUT", "/internal/jobs/"+topic.ID+"/blobs/cache?version="+version+"&key=written&namespace=refs/heads/trunk", "topic-only", map[string]string{"Authorization": "Bearer token-" + topic.ID})
	if code != 200 {
		t.Fatal(code, string(body))
	}
	if code, _ := h.runtimeRequest("GET", "blobs/cache?version="+version+"&key=written", main, nil); code != 404 {
		t.Fatal("default read topic write", code)
	}
	if code, body := h.runtimeRequest("GET", "blobs/cache?version="+version+"&key=written", topic, nil); code != 200 || string(body) != "topic-only" {
		t.Fatal(code, string(body))
	}
}
