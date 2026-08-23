package httpclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/run/plan"
	"github.com/shitamachi/forgelet/internal/runtime/executor"
	"github.com/shitamachi/forgelet/internal/security/identity"
)

type capture struct {
	Method string
	Path   string
	Auth   string
	Body   []byte
}

func fakeControlPlane(t *testing.T, status int, resp map[string]any) (*capture, *httptest.Server) {
	t.Helper()
	cap := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.Method, cap.Path, cap.Auth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		cap.Body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"sensitive-detail-token"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return cap, srv
}

func testIdent() identity.Identity {
	return identity.Identity{Audience: identity.Audience, JobRunID: model.JobRunID("01JJOB000000000000000000A")}
}

func TestFetchPlanProtocol(t *testing.T) {
	cap, srv := fakeControlPlane(t, http.StatusOK, map[string]any{
		"jobRunId": "01JJOB000000000000000000A", "sha": "abc", "ref": "refs/heads/main",
		"runnerClass": "small", "steps": []map[string]any{{"id": "s", "run": map[string]any{"script": "echo hi"}}},
	})
	c := New(srv.URL, "tok-1", srv.Client())

	p, err := c.FetchPlan(context.Background(), testIdent())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if cap.Method != http.MethodGet || cap.Path != "/internal/jobruns/01JJOB000000000000000000A/plan" {
		t.Errorf("request %s %s", cap.Method, cap.Path)
	}
	if cap.Auth != "Bearer tok-1" {
		t.Errorf("auth = %q", cap.Auth)
	}
	if len(p.Steps) != 1 || p.Steps[0].Run.Script != "echo hi" {
		t.Errorf("plan = %+v", p)
	}
}

func TestFetchSecretsProtocol(t *testing.T) {
	cap, srv := fakeControlPlane(t, http.StatusOK, map[string]any{
		"repository/TOKEN": "ghs_secret_value",
	})
	c := New(srv.URL, "tok-1", srv.Client())

	refs := []plan.SecretRef{{Scope: "repository", Name: "TOKEN", Env: "TOKEN"}}
	got, err := c.FetchSecrets(context.Background(), testIdent(), refs)
	if err != nil {
		t.Fatalf("fetch secrets: %v", err)
	}
	if got["repository/TOKEN"] != "ghs_secret_value" {
		t.Errorf("secrets = %v", got)
	}
	// The request body contains references only — never values.
	var body []map[string]any
	if err := json.Unmarshal(cap.Body, &body); err != nil {
		t.Fatalf("body not json: %v", err)
	}
	if len(body) != 1 || body[0]["scope"] != "repository" || body[0]["name"] != "TOKEN" || len(body[0]) != 2 {
		t.Errorf("request body = %s", cap.Body)
	}
}

func TestReportJobProtocol(t *testing.T) {
	cap, srv := fakeControlPlane(t, http.StatusOK, map[string]any{})
	c := New(srv.URL, "tok-1", srv.Client())

	res := executor.JobResult{JobRunID: "01JJOB000000000000000000A", Success: true}
	if err := c.ReportJob(context.Background(), testIdent(), res); err != nil {
		t.Fatalf("report: %v", err)
	}
	if cap.Method != http.MethodPost || cap.Path != "/internal/jobruns/01JJOB000000000000000000A/status" {
		t.Errorf("request %s %s", cap.Method, cap.Path)
	}
	if !strings.Contains(string(cap.Body), `"success":true`) {
		t.Errorf("body = %s", cap.Body)
	}
}

func TestTypedErrorsAndNoBodyLeak(t *testing.T) {
	for _, tc := range []struct {
		status    int
		retryable bool
	}{
		{http.StatusUnauthorized, false},
		{http.StatusForbidden, false},
		{http.StatusNotFound, false},
		{http.StatusInternalServerError, true},
		{http.StatusTooManyRequests, true},
	} {
		_, srv := fakeControlPlane(t, tc.status, nil)
		c := New(srv.URL, "tok", srv.Client())
		_, err := c.FetchPlan(context.Background(), testIdent())
		var ce *ClientError
		if !errors.As(err, &ce) {
			t.Errorf("status %d: error %T (%v), want ClientError", tc.status, err, err)
			continue
		}
		if ce.Retryable() != tc.retryable {
			t.Errorf("status %d: retryable = %v, want %v", tc.status, ce.Retryable(), tc.retryable)
		}
		// Error messages must not echo response bodies (secret-safe).
		if strings.Contains(err.Error(), "sensitive-detail-token") {
			t.Errorf("status %d: error leaks body: %v", tc.status, err)
		}
	}
}
