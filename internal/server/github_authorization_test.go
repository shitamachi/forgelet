package server

import (
	"testing"
	"time"

	gh "github.com/shitamachi/forgelet/internal/provider/github"
)

func TestRepositoryAuthorizationUsesCompleteIdentityAndQuarantine(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*GitHubAuthorization, *RepositoryConfig)
	}{
		{"authorized", "authorized", func(*GitHubAuthorization, *RepositoryConfig) {}},
		{"suspended", "suspended", func(s *GitHubAuthorization, _ *RepositoryConfig) {
			now := time.Now()
			s.Catalog.Installations[0].SuspendedAt = &now
		}},
		{"missing installation", "not_authorized", func(s *GitHubAuthorization, _ *RepositoryConfig) { s.Catalog.Installations = nil }},
		{"missing repository", "not_authorized", func(s *GitHubAuthorization, _ *RepositoryConfig) { s.Catalog.Repositories = nil }},
		{"renamed same ID", "renamed", func(s *GitHubAuthorization, _ *RepositoryConfig) { s.Catalog.Repositories[0].Name = "me/renamed" }},
		{"same name different ID", "not_authorized", func(s *GitHubAuthorization, _ *RepositoryConfig) { s.Catalog.Repositories[0].ID = 456 }},
		{"another installation", "not_authorized", func(s *GitHubAuthorization, _ *RepositoryConfig) { s.Catalog.Repositories[0].InstallationID = 99 }},
		{"permission removed", "insufficient_permissions", func(s *GitHubAuthorization, _ *RepositoryConfig) {
			delete(s.Catalog.Installations[0].Permissions, "checks")
		}},
		{"archived", "archived", func(s *GitHubAuthorization, _ *RepositoryConfig) { s.Catalog.Repositories[0].Archived = true }},
		{"disabled upstream", "disabled", func(s *GitHubAuthorization, _ *RepositoryConfig) { s.Catalog.Repositories[0].Disabled = true }},
		{"uncertain event quarantines", "pending", func(s *GitHubAuthorization, _ *RepositoryConfig) { s.Quarantined = []int64{42} }},
		{"read failure alone preserves grant", "authorized", func(s *GitHubAuthorization, _ *RepositoryConfig) { s.Error = "refresh unavailable"; s.Pending = true }},
		{"legacy name identity", "authorized", func(_ *GitHubAuthorization, r *RepositoryConfig) { r.RepositoryID = 0; r.Name = "ME/REPO" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := GitHubAuthorization{Catalog: &gh.Catalog{Installations: []gh.Installation{{ID: 42, Permissions: map[string]string{"contents": "read", "checks": "write"}}}, Repositories: []gh.Repository{{ID: 123, Name: "me/repo", InstallationID: 42}}}}
			repo := RepositoryConfig{Name: "me/repo", RepositoryID: 123, InstallationID: 42, Enabled: true}
			tc.change(&snapshot, &repo)
			got := repositoryAuthorization(&snapshot, repo, 42)
			if got.State != tc.want {
				t.Fatalf("got %+v want %s", got, tc.want)
			}
		})
	}
	if got := repositoryAuthorization(nil, RepositoryConfig{}, 42); got.State != "unknown" {
		t.Fatal(got)
	}
}
