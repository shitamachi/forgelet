package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	gh "github.com/shitamachi/forgelet/internal/provider/github"
	"github.com/shitamachi/forgelet/internal/run"
)

type onboardingFake struct {
	*fakeProvider
	catalog gh.Catalog
	err     error
	sha     string
}

func (p *onboardingFake) Discover(context.Context) (gh.Catalog, error) { return p.catalog, p.err }
func (p *onboardingFake) DefaultCommit(context.Context, gh.Repository) (string, error) {
	return p.sha, nil
}
func onboardingHarness(t *testing.T) (*harness, *onboardingFake) {
	t.Helper()
	h := newHarness(t, 2)
	p := &onboardingFake{fakeProvider: h.provider, sha: testSHA, catalog: gh.Catalog{Repositories: []gh.Repository{{ID: 123, Name: "me/repo", InstallationID: 42, DefaultBranch: "main"}}, Installations: []gh.Installation{{ID: 42, Permissions: map[string]string{"contents": "read", "checks": "write"}}}}}
	h.server.provider = p
	return h, p
}
func TestOnboardingEnablePreviewRunAndRestart(t *testing.T) {
	h, p := onboardingHarness(t)
	if code, _ := h.admin("POST", "/api/github/preview", onboardingInput{RepositoryID: 123}); code != 422 {
		t.Fatal(code)
	}
	code, body := h.admin("POST", "/api/github/enable", onboardingInput{RepositoryID: 123, Version: 0})
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if code, _ = h.admin("POST", "/api/github/enable", onboardingInput{RepositoryID: 123, Version: 0}); code != 409 {
		t.Fatal(code)
	}
	code, body = h.admin("POST", "/api/github/preview", onboardingInput{RepositoryID: 123})
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var preview onboardingPreview
	if err := json.Unmarshal(body, &preview); err != nil {
		t.Fatal(err)
	}
	if preview.SHA != testSHA || preview.Ref != "refs/heads/main" || len(preview.Workflows) != 1 || !preview.Workflows[0].Runnable {
		t.Fatalf("bad preview: %s", body)
	}
	input := onboardingInput{RepositoryID: 123, SHA: preview.SHA, Ref: preview.Ref, Path: preview.Workflows[0].Path, RequestID: "unique-request-12345"}
	p.sha = strings.Repeat("b", 40) // Branch moving after preview must not change execution.
	code, body = h.admin("POST", "/api/github/run", input)
	if code != 201 {
		t.Fatalf("%d %s", code, body)
	}
	var item run.Run
	if err := json.Unmarshal(body, &item); err != nil {
		t.Fatal(err)
	}
	if item.SHA != testSHA {
		t.Fatal(item.SHA)
	}
	code, body = h.admin("POST", "/api/github/run", input)
	if code != 201 {
		t.Fatalf("retry %d %s", code, body)
	}
	var repeated run.Run
	_ = json.Unmarshal(body, &repeated)
	if item.ID != repeated.ID {
		t.Fatal("duplicate run")
	}
	h.http.Close()
	h.start()
	m := getManagement(t, h)
	if m.Repositories[0].RepositoryID != 123 || m.Repositories[0].InstallationID != 42 {
		t.Fatalf("binding lost: %+v", m)
	}
}
func TestOnboardingDiagnosticsAndRevocation(t *testing.T) {
	for _, mode := range []string{"unknown-id", "partial", "permissions", "revoked-after-preview", "unknown-profile", "unsupported-field", "filtered", "missing-file", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			h, p := onboardingHarness(t)
			if code, _ := h.admin("POST", "/api/github/enable", onboardingInput{RepositoryID: 123}); code != 200 {
				t.Fatal(code)
			}
			switch mode {
			case "unknown-id":
				if code, _ := h.admin("POST", "/api/github/enable", onboardingInput{RepositoryID: 999, Version: 1}); code != 422 {
					t.Fatal(code)
				}
				return
			case "partial":
				p.err = errors.New("API offline")
			case "permissions":
				p.catalog.Installations[0].Permissions["checks"] = "read"
			case "revoked-after-preview":
				p.catalog.Repositories = nil
			case "unknown-profile":
				p.files = map[string][]byte{".github/workflows/check.yml": []byte(strings.ReplaceAll(testWorkflow, "small", "ubuntu-latest"))}
			case "unsupported-field":
				p.files = map[string][]byte{".github/workflows/check.yml": []byte("on: push\njobs: {a: {runs-on: small, unsupported: true}}")}
			case "filtered":
				p.files = map[string][]byte{".github/workflows/check.yml": []byte("on: {push: {branches: [other]}}\njobs: {a: {runs-on: small, steps: [{run: echo ok}]}}")}
			case "missing-file":
				p.files = map[string][]byte{}
			case "disabled":
				m := getManagement(t, h)
				m.Repositories[0].Enabled = false
				putManagement(t, h, m, 200)
			}
			code, body := h.admin("POST", "/api/github/preview", onboardingInput{RepositoryID: 123})
			if mode == "unknown-profile" || mode == "unsupported-field" || mode == "filtered" || mode == "missing-file" {
				if code != 200 {
					t.Fatalf("%d %s", code, body)
				}
				var preview onboardingPreview
				_ = json.Unmarshal(body, &preview)
				for _, file := range preview.Workflows {
					if file.Runnable || file.Diagnostic == "" {
						t.Fatalf("missing diagnostic %s", body)
					}
				}
			} else if code != 422 {
				t.Fatalf("expected rejection %d %s", code, body)
			}
			code, body = h.admin("POST", "/api/github/run", onboardingInput{RepositoryID: 123, SHA: testSHA, Ref: "refs/heads/main", Path: ".github/workflows/check.yml", RequestID: "unique-request-12345"})
			if code != 422 {
				t.Fatalf("invalid run accepted %d %s", code, body)
			}
			runs, err := h.store.ListRuns(t.Context())
			if err != nil || len(runs) != 0 {
				t.Fatal(runs, err)
			}
			if mode == "partial" && getManagement(t, h).Version != 1 {
				t.Fatal("failed discovery changed configuration")
			}
		})
	}
}

func TestOnboardingRenameAndSuspensionPreserveConfiguration(t *testing.T) {
	h, p := onboardingHarness(t)
	if code, _ := h.admin("POST", "/api/github/enable", onboardingInput{RepositoryID: 123}); code != 200 {
		t.Fatal(code)
	}
	p.catalog.Repositories[0].Name = "me/renamed"
	if code, _ := h.admin("POST", "/api/github/enable", onboardingInput{RepositoryID: 123, Version: 1}); code != 409 {
		t.Fatal(code)
	}
	if m := getManagement(t, h); m.Version != 1 || m.Repositories[0].Name != "me/repo" {
		t.Fatal(m)
	}
	p.catalog.Repositories[0].Name = "me/repo"
	now := h.server.now()
	p.catalog.Installations[0].SuspendedAt = &now
	if code, _ := h.admin("POST", "/api/github/preview", onboardingInput{RepositoryID: 123}); code != 422 {
		t.Fatal(code)
	}
	// Query parameters cannot bind a different installation, and unknown JSON
	// fields are rejected rather than treated as installation authority.
	if code, _ := h.admin("POST", "/api/github/enable", map[string]any{"repository_id": 123, "installation_id": 999}); code != 400 {
		t.Fatal(code)
	}
	if m := getManagement(t, h); m.Version != 1 || m.Repositories[0].InstallationID != 42 {
		t.Fatal(m)
	}
}
