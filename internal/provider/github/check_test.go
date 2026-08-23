package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/shitamachi/forgelet/internal/report"
	"github.com/shitamachi/forgelet/internal/run/model"
)

type fakeCheckAPI struct {
	mu        sync.Mutex
	nextID    int64
	checks    map[string]map[string]json.RawMessage // externalID -> latest payload
	listCalls int
	posts     int
	patches   int
}

func newFakeCheckAPI() *fakeCheckAPI {
	return &fakeCheckAPI{checks: map[string]map[string]json.RawMessage{}, nextID: 100}
}

func (f *fakeCheckAPI) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/check-runs"):
			f.listCalls++
			runs := make([]string, 0, len(f.checks))
			for ext := range f.checks {
				runs = append(runs, `{"id":`+itoa(f.nextID)+`,"external_id":"`+ext+`","status":"queued"}`)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"total_count":` + itoa(int64(len(runs))) + `,"check_runs":[` + strings.Join(runs, ",") + `]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/check-runs"):
			f.posts++
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			ext := payload["external_id"].(string)
			f.nextID++
			f.checks[ext] = normalize(payload)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":` + itoa(f.nextID) + `}`))
		case r.Method == http.MethodPatch:
			f.patches++
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			ext := payload["external_id"].(string)
			f.checks[ext] = normalize(payload)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":1}`))
		default:
			http.Error(w, "not found: "+r.URL.Path, http.StatusNotFound)
		}
	})
}

func normalize(p map[string]any) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for k, v := range p {
		b, _ := json.Marshal(v)
		out[k] = b
	}
	return out
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

type staticToken struct{}

func (staticToken) Token(context.Context) (string, error) { return "ghs_x", nil }

func testRun() model.RunRecord {
	return model.RunRecord{
		ID: model.RunID("01JRUN000000000000000000X"),
		Event: model.Event{
			Provider:   "github",
			Repository: model.RepositoryRef{Provider: "github", Owner: "shitamachi", Name: "forgelet"},
			SHA:        "abc123",
		},
	}
}

func TestCheckReporterLifecycleUpsert(t *testing.T) {
	api := newFakeCheckAPI()
	srv := httptest.NewServer(api.handler())
	defer srv.Close()
	r := NewCheckReporter(srv.URL, srv.Client(), staticToken{})
	ctx := context.Background()
	run := testRun()

	queued := report.Check{RunID: run.ID, JobRunID: "01JJOB", Name: "test",
		ExternalID: "01JJOB", DetailsURL: "https://ci.example.com/runs/01JRUN/jobs/test",
		Status: report.StatusQueued}
	if err := r.Report(ctx, run, queued); err != nil {
		t.Fatalf("report queued: %v", err)
	}

	progress := queued
	progress.Status = report.StatusProgress
	if err := r.Report(ctx, run, progress); err != nil {
		t.Fatalf("report in_progress: %v", err)
	}

	done := queued
	done.Status = report.StatusCompleted
	done.Conclusion = report.ConclusionSuccess
	if err := r.Report(ctx, run, done); err != nil {
		t.Fatalf("report success: %v", err)
	}

	// AC 5: three reports -> exactly one check run record (create + patches).
	if api.posts != 1 {
		t.Errorf("creates = %d, want 1", api.posts)
	}
	if api.patches != 2 {
		t.Errorf("patches = %d, want 2", api.patches)
	}
	final := api.checks["01JJOB"]
	if string(final["status"]) != `"completed"` || string(final["conclusion"]) != `"success"` {
		t.Errorf("final state wrong: %v", final)
	}
	if string(final["external_id"]) != `"01JJOB"` {
		t.Errorf("external_id missing: %v", final)
	}
	if string(final["details_url"]) != `"https://ci.example.com/runs/01JRUN/jobs/test"` {
		t.Errorf("details_url wrong: %v", final)
	}
	if string(final["head_sha"]) != `"abc123"` {
		t.Errorf("head_sha wrong: %v", final)
	}
}

func TestCheckReporterRequiresRunContext(t *testing.T) {
	api := httptest.NewServer(newFakeCheckAPI().handler())
	defer api.Close()
	r := NewCheckReporter(api.URL, api.Client(), staticToken{})

	noRepo := testRun()
	noRepo.Event.Repository = model.RepositoryRef{}
	if err := r.Report(context.Background(), noRepo, report.Check{ExternalID: "x", Status: report.StatusQueued}); err == nil {
		t.Error("missing repository must fail")
	}
	noSHA := testRun()
	noSHA.Event.SHA = ""
	if err := r.Report(context.Background(), noSHA, report.Check{ExternalID: "x", Status: report.StatusQueued}); err == nil {
		t.Error("missing sha must fail")
	}
}

func TestCheckReporterAPIFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	r := NewCheckReporter(srv.URL, srv.Client(), staticToken{})
	err := r.Report(context.Background(), testRun(), report.Check{ExternalID: "x", Status: report.StatusQueued})
	if err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("want 500 error, got %v", err)
	}
}
