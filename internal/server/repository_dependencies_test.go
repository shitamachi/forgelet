package server

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	gh "github.com/shitamachi/forgelet/internal/provider/github"
	"github.com/shitamachi/forgelet/internal/run"
)

type dependencySourceProvider struct {
	*onboardingFake
	revoke       func()
	dependencies []string
	calls        int
}

func (p *dependencySourceProvider) SourceToken(_ context.Context, _ int64, source string, dependencies []string) (string, error) {
	p.calls++
	p.dependencies = append([]string(nil), dependencies...)
	if source != "me/repo" {
		return "", context.Canceled
	}
	if p.revoke != nil {
		p.revoke()
	}
	return "scoped-private-dependency-token", nil
}

func TestRepositoryDependencyPolicySnapshot(t *testing.T) {
	policies, err := normalizeRepositoryDependencies(map[string][]string{"me/repo": {"me/B", "me/a", "me/a", "me/repo"}})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: Config{RepositoryDependencies: policies}}
	p := run.Plan{Repository: "me/repo", Permissions: map[string]string{"contents": "read"}}
	if err = s.repositoryDependencyAdmission(&p); err != nil {
		t.Fatal(err)
	}
	if !p.RepositoryDependenciesSelected || !reflect.DeepEqual(p.RepositoryDependencies, []string{"me/a", "me/b"}) {
		t.Fatal(p.RepositoryDependencies)
	}
	s.cfg.RepositoryDependencies["me/repo"] = []string{"me/a"}
	if err = s.repositoryDependencyAdmission(&p); err == nil {
		t.Fatal("rerun expanded or revoked a frozen dependency policy")
	}
	if err = s.requireSourceDependencies(p, 42); err == nil {
		t.Fatal("changed policy allowed credential delivery")
	}
	if err = s.repositoryDependencyAdmission(&run.Plan{Repository: "me/repo", RepositoryDependenciesSelected: true}); err == nil {
		t.Fatal("old empty snapshot gained new permissions")
	}
	if err = s.repositoryDependencyAdmission(&run.Plan{Repository: "me/repo", Permissions: map[string]string{}}); err == nil {
		t.Fatal("dependencies ignored contents:none")
	}
	for _, deps := range [][]string{{"other/repo"}, {"me/../repo"}, make([]string, 17)} {
		if _, err = normalizeRepositoryDependencies(map[string][]string{"me/repo": deps}); err == nil {
			t.Fatal("invalid policy accepted", deps)
		}
	}
}

func TestSourceDependencyCredentialAuthorization(t *testing.T) {
	for _, boundary := range []string{"allowed", "before mint", "during mint", "wrong installation"} {
		t.Run(boundary, func(t *testing.T) {
			h, base := onboardingHarness(t)
			base.catalog.Repositories = append(base.catalog.Repositories, gh.Repository{ID: 456, Name: "me/sdk", InstallationID: 42})
			if _, err := h.server.refreshGitHubAuthorization(t.Context()); err != nil {
				t.Fatal(err)
			}
			p := &dependencySourceProvider{onboardingFake: base}
			h.server.provider = p
			h.server.cfg.RepositoryDependencies = map[string][]string{"me/repo": {"me/sdk"}}
			e := run.Event{Name: "push", Repository: "me/repo", InstallationID: 42, SHA: testSHA, Ref: "refs/heads/main", DeliveryID: "dependency-auth", Payload: json.RawMessage(`{}`)}
			item, err := h.server.createRun(t.Context(), e, ".github/workflows/ci.yml", []byte("on: push\njobs:\n  test:\n    runs-on: small\n    steps:\n      - uses: actions/checkout@v4\n"))
			if err != nil {
				t.Fatal(err)
			}
			if err = h.server.Tick(t.Context()); err != nil {
				t.Fatal(err)
			}
			item, _ = h.store.GetRun(t.Context(), item.ID)
			job := item.Jobs[0]
			if code, b := h.runtimeRequest("POST", "claim", job, nil); code != 200 {
				t.Fatalf("claim: %d %s", code, b)
			}
			revoke := func() {
				_, err := h.server.mutateAuthorization(t.Context(), func(a *GitHubAuthorization) error { a.Catalog.Repositories = a.Catalog.Repositories[:1]; return nil })
				if err != nil {
					t.Fatal(err)
				}
			}
			if boundary == "before mint" {
				revoke()
			}
			if boundary == "during mint" {
				p.revoke = revoke
			}
			if boundary == "wrong installation" {
				_, err = h.server.mutateAuthorization(t.Context(), func(a *GitHubAuthorization) error { a.Catalog.Repositories[1].InstallationID = 99; return nil })
				if err != nil {
					t.Fatal(err)
				}
			}
			code, b := h.runtimeRequest("GET", "credentials", job, nil)
			if boundary == "allowed" {
				if code != 200 || !strings.Contains(string(b), "scoped-private-dependency-token") || !reflect.DeepEqual(p.dependencies, []string{"me/sdk"}) {
					t.Fatalf("credential: %d %s dependencies=%v", code, b, p.dependencies)
				}
			} else if code != 403 || strings.Contains(string(b), "scoped-private-dependency-token") {
				t.Fatalf("revoked dependency leaked: %d %s", code, b)
			}
			if (boundary == "before mint" || boundary == "wrong installation") && p.calls != 0 {
				t.Fatal("minted a forbidden credential")
			}
		})
	}
}
