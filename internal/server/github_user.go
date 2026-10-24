package server

import (
	"errors"
	"net/http"
	"strings"

	gh "github.com/shitamachi/forgelet/internal/provider/github"
)

func (s *Server) githubUserRepositories(w http.ResponseWriter, r *http.Request) {
	token, err := s.githubUserToken(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	repos, err := s.cfg.Auth.GitHub.Repositories(r.Context(), token.AccessToken)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, 200, repos)
}
func (s *Server) githubUserEnable(w http.ResponseWriter, r *http.Request) {
	var input onboardingInput
	if err := decode(w, r, &input, 16384); err != nil || input.RepositoryID <= 0 {
		http.Error(w, "repository_id required", http.StatusBadRequest)
		return
	}
	// Reject stale edits before remote reads. Consent never changes local trust;
	// enableRepository checks the same version again before saving.
	if input.Version != s.management().Version {
		http.Error(w, "configuration changed; refresh before enabling", http.StatusConflict)
		return
	}
	token, err := s.githubUserToken(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	repos, err := s.cfg.Auth.GitHub.Repositories(r.Context(), token.AccessToken)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	var owner, name string
	for _, repo := range repos {
		if repo.ID == input.RepositoryID && repo.Permissions.Admin && !repo.Archived && !repo.Disabled {
			owner = repo.Owner.Login
			name = repo.Name
		}
	}
	if owner == "" || name == "" {
		http.Error(w, "repository administrator access required", http.StatusForbidden)
		return
	}
	catalog, err := s.catalog(r.Context())
	if err != nil {
		http.Error(w, "cannot verify this GitHub App installation", http.StatusBadGateway)
		return
	}
	var installation *gh.Installation
	for i := range catalog.Installations {
		if strings.EqualFold(catalog.Installations[i].Account.Login, owner) {
			installation = &catalog.Installations[i]
			break
		}
	}
	// OAuth App tokens can discover user repositories, but cannot change a
	// GitHub App installation. Never send them to the installation mutation API.
	// Return an actionable consent step and only enable after App authority agrees.
	authorize := func(reason, address string) {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"status": "authorization_required", "reason": reason,
			"repository_id": input.RepositoryID, "repository": name,
			"authorize_url": address,
		})
	}
	if installation == nil {
		authorize("installation_required", catalog.InstallURL)
		return
	}
	if installation.SuspendedAt != nil {
		authorize("installation_suspended", installation.ManageURL)
		return
	}
	if (installation.Permissions["contents"] != "read" && installation.Permissions["contents"] != "write") || installation.Permissions["checks"] != "write" {
		authorize("installation_permissions", installation.ManageURL)
		return
	}
	granted := false
	for _, repo := range catalog.Repositories {
		if repo.ID == input.RepositoryID && repo.InstallationID == installation.ID && repo.Name == name {
			granted = true
		}
	}
	if !granted {
		authorize("repository_not_granted", installation.ManageURL)
		return
	}
	// Re-read current App authority; OAuth discovery never substitutes for
	// permission to use the App's clone, Checks or centralized webhook.
	repo, err := s.authorizedRepository(r.Context(), input.RepositoryID)
	if err != nil || repo.InstallationID != installation.ID || repo.Name != name {
		if err == nil {
			err = errors.New("GitHub repository binding changed; refresh")
		}
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	next, err := s.enableRepository(r.Context(), repo, input.Version)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, 200, next)
}
