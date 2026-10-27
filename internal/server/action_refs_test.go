package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/action"
	"github.com/shitamachi/forgelet/internal/kube"
	"github.com/shitamachi/forgelet/internal/run"
	"github.com/shitamachi/forgelet/internal/storage"
)

// Existing HTTP/SQLite tests are hermetic: their fixed legacy versions do not
// query GitHub. The resolver-specific tests below exercise moving remote refs.
func fixtureActionResolver(t *testing.T) *action.Resolver {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/contents/") {
			_, _ = w.Write([]byte("runs: {using: node20, main: main.js}\n"))
			return
		}
		prefix := strings.TrimPrefix(r.URL.Path, "/repos/")
		repository, ref, ok := strings.Cut(prefix, "/git/ref/tags/")
		sha := action.ActVersions[repository+"@"+ref]
		if !ok || sha == "" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"type": "commit", "sha": sha}})
	}))
	t.Cleanup(api.Close)
	return &action.Resolver{Client: api.Client(), APIURL: api.URL}
}

func TestActionLockSurvivesSQLiteRestartRerunAndDuplicateDelivery(t *testing.T) {
	h := newHarness(t, 1)
	var moved atomic.Bool
	var reads atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if strings.Contains(r.URL.Path, "/contents/") {
			_, _ = w.Write([]byte("runs: {using: node24, main: index.js}\n"))
			return
		}
		sha := strings.Repeat("a", 40)
		if moved.Load() {
			sha = strings.Repeat("b", 40)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"type": "commit", "sha": sha}})
	}))
	defer api.Close()
	h.http.Close()
	h.cfg.ActionResolver = &action.Resolver{Client: api.Client(), APIURL: api.URL}
	h.cfg.Profiles["actions"] = kube.Profile{Image: "executor:act", Act: &kube.ActProfile{}}
	h.start()
	source := `on: push
jobs:
  node:
    runs-on: actions
    steps:
      - uses: owner/action@latest
`
	first := h.manual(source)
	if first.Jobs[0].Plan.Steps[0].Action.SHA != strings.Repeat("a", 40) {
		t.Fatal(first)
	}
	moved.Store(true)
	if code, body := h.admin(http.MethodPost, "/api/runs/"+first.ID+"/cancel", nil); code != 200 {
		t.Fatal(code, string(body))
	}
	h.http.Close()
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	h.store, err = storage.Open(t.Context(), h.path)
	if err != nil {
		t.Fatal(err)
	}
	h.start()
	before := reads.Load()
	duplicate, err := h.server.createRunFiles(t.Context(), run.Event{DeliveryID: first.DeliveryID, Repository: first.Repository}, first.WorkflowPath, []byte(source), nil)
	if err != nil || duplicate.ID != first.ID || reads.Load() != before {
		t.Fatal("duplicate fetched mutable refs again", duplicate, err)
	}
	code, body := h.admin(http.MethodPost, "/api/runs/"+first.ID+"/rerun", nil)
	var rerun run.Run
	if code != 201 || json.Unmarshal(body, &rerun) != nil {
		t.Fatal(code, string(body))
	}
	if reads.Load() != before || rerun.Jobs[0].Plan.Steps[0].Action.SHA != strings.Repeat("a", 40) || rerun.Jobs[0].Plan.ActionPins["owner/action@latest"] != strings.Repeat("a", 40) {
		t.Fatal("rerun drifted", rerun)
	}
	second := h.manual(source)
	if second.Jobs[0].Plan.Steps[0].Action.SHA != strings.Repeat("b", 40) || reads.Load() <= before {
		t.Fatal("new run did not refresh tag", second)
	}
}

func TestOnboardingFirstRunResolvesOnceAndRetryUsesSavedLock(t *testing.T) {
	h, p := onboardingHarness(t)
	h.server.cfg.Profiles["actions"] = kube.Profile{Image: "executor:act", Act: &kube.ActProfile{}}
	p.files = map[string][]byte{".github/workflows/ci.yml": []byte("on: push\njobs:\n  check:\n    runs-on: actions\n    steps:\n      - uses: owner/action@v1\n")}
	var requests atomic.Int32
	var unavailable atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if unavailable.Load() {
			w.WriteHeader(503)
			return
		}
		if strings.Contains(r.URL.Path, "/contents/") {
			_, _ = w.Write([]byte("runs: {using: node20, main: index.js}\n"))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"type": "commit", "sha": strings.Repeat("a", 40)}})
	}))
	defer api.Close()
	h.server.cfg.ActionResolver = &action.Resolver{Client: api.Client(), APIURL: api.URL}
	if code, body := h.admin("POST", "/api/github/enable", onboardingInput{RepositoryID: 123, Version: 0}); code != 200 {
		t.Fatal(code, string(body))
	}
	input := onboardingInput{RepositoryID: 123, SHA: testSHA, Ref: "refs/heads/main", Path: ".github/workflows/ci.yml", RequestID: "immutable-action-request-106b"}
	code, body := h.admin("POST", "/api/github/run", input)
	if code != 201 || requests.Load() != 2 {
		t.Fatal("first run resolved twice", code, string(body), requests.Load())
	}
	unavailable.Store(true)
	code, repeated := h.admin("POST", "/api/github/run", input)
	var first, second run.Run
	if code != 201 || json.Unmarshal(body, &first) != nil || json.Unmarshal(repeated, &second) != nil || first.ID != second.ID || requests.Load() != 2 {
		t.Fatal("retry fetched moved/unavailable refs", code, string(repeated), requests.Load())
	}
}

func TestPublicActionOutageRetriesWebhookWithoutPartialRun(t *testing.T) {
	h := newHarness(t, 1)
	h.server.cfg.Profiles["actions"] = kube.Profile{Image: "executor:act", Act: &kube.ActProfile{}}
	h.provider.files = map[string][]byte{".github/workflows/ci.yml": []byte("on: push\njobs:\n  check:\n    runs-on: actions\n    steps:\n      - uses: owner/action@v1\n")}
	var recovered atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !recovered.Load() {
			w.WriteHeader(429)
			return
		}
		if strings.Contains(r.URL.Path, "/contents/") {
			_, _ = w.Write([]byte("runs: {using: node24, main: index.js}\n"))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"type": "commit", "sha": strings.Repeat("a", 40)}})
	}))
	defer api.Close()
	h.server.cfg.ActionResolver = &action.Resolver{Client: api.Client(), APIURL: api.URL}
	if code, body := h.webhook("public-source-outage-106b", nil); code != 202 {
		t.Fatal(code, string(body))
	}
	if err := h.server.Tick(t.Context()); !errors.Is(err, action.ErrUnavailable) {
		t.Fatal("rate limit was not retryable", err)
	}
	pending, err := h.store.PendingEvents(t.Context(), time.Now().Add(time.Hour))
	if err != nil || len(pending) != 1 {
		t.Fatal("event was discarded", pending, err)
	}
	if runs, err := h.store.ListRuns(t.Context()); err != nil || len(runs) != 0 {
		t.Fatal("partial run persisted", runs, err)
	}
	recovered.Store(true)
	h.server.now = func() time.Time { return time.Now().Add(time.Hour) }
	h.tick()
	runs, err := h.store.ListRuns(t.Context())
	if err != nil || len(runs) != 1 || runs[0].Jobs[0].Plan.ActionPins["owner/action@v1"] != strings.Repeat("a", 40) {
		t.Fatal(runs, err)
	}
}

func TestGitHubAPIOriginIsValidatedAndSnapshotted(t *testing.T) {
	for _, raw := range []string{"file:///etc/private", "https://user:secret@example.com", "https://example.com?q=secret", "http://external.example"} {
		if err := validateGitHubAPIURL(raw); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal(raw, err)
		}
	}
	h := newHarness(t, 1)
	h.server.cfg.GitHubAPIURL = "https://proxy.example/api/v3/"
	first := h.manual(testWorkflow)
	api := first.Jobs[0].Plan.GitHubAPIURL
	values, _ := first.Jobs[0].Plan.Contexts()
	if api != "https://proxy.example/api/v3" || values["github.api_url"] != api {
		t.Fatal(first)
	}
	if code, body := h.admin(http.MethodPost, "/api/runs/"+first.ID+"/cancel", nil); code != 200 {
		t.Fatal(code, string(body))
	}
	h.server.cfg.GitHubAPIURL = "https://changed.example"
	code, body := h.admin(http.MethodPost, "/api/runs/"+first.ID+"/rerun", nil)
	var again run.Run
	if code != 201 || json.Unmarshal(body, &again) != nil || again.Jobs[0].Plan.GitHubAPIURL != api {
		t.Fatal(code, string(body))
	}
}

func TestTemporaryActionSourceFailureIsVisibleWithoutLeakingUpstreamDetails(t *testing.T) {
	h := newHarness(t, 1)
	h.server.cfg.Profiles["actions"] = kube.Profile{Image: "executor:act", Act: &kube.ActProfile{}}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "private upstream diagnostic", http.StatusServiceUnavailable)
	}))
	defer api.Close()
	h.server.cfg.ActionResolver = &action.Resolver{Client: api.Client(), APIURL: api.URL}
	code, body := h.admin(http.MethodPost, "/api/runs", map[string]any{"repository": "me/repo", "sha": testSHA, "workflow": "on: push\njobs:\n  check:\n    runs-on: actions\n    steps:\n      - uses: owner/action@v1\n"})
	if code != 503 || !strings.Contains(string(body), "temporarily unavailable") || strings.Contains(string(body), "private upstream") {
		t.Fatal(code, string(body))
	}
}
