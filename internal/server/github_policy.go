package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var errRepositoryAuthorization = errors.New("GitHub repository authorization denied")
var errRepositoryAuthorizationPending = errors.New("GitHub repository authorization pending")

// Repositories served by a discovery-capable App never trust an event's
// installation ID as proof of access. Legacy name-only bindings still require
// membership in the complete App catalog.
func (s *Server) repositoryAccess(name string, installation int64) RepositoryAuthorization {
	if _, ok := s.provider.(onboardingProvider); !ok {
		return RepositoryAuthorization{State: "authorized"}
	}
	snapshot := s.authorization.Load()
	for _, repo := range s.management().Repositories {
		if !strings.EqualFold(repo.Name, name) {
			continue
		}
		expected := installationForRepository(repo, snapshot)
		if installation != 0 && expected != 0 && installation != expected {
			return RepositoryAuthorization{State: "not_authorized"}
		}
		return repositoryAuthorization(snapshot, repo, expected)
	}
	return RepositoryAuthorization{State: "not_authorized"}
}
func (s *Server) requireRepositoryAccess(name string, installation int64) error {
	state := s.repositoryAccess(name, installation).State
	if state != "authorized" {
		if state == "unknown" || state == "pending" {
			return fmt.Errorf("%w: %s", errRepositoryAuthorizationPending, state)
		}
		return fmt.Errorf("%w: %s", errRepositoryAuthorization, state)
	}
	return nil
}

func (s *Server) reconcileRepositoryAuthorization(ctx context.Context) ([]string, error) {
	var allowed []string
	for _, repo := range s.management().Repositories {
		if !repo.Enabled {
			continue
		}
		state := s.repositoryAccess(repo.Name, 0).State
		switch state {
		case "authorized":
			allowed = append(allowed, repo.Name)
		case "unknown", "pending": // An unavailable or superseded read is not a revocation.
		default:
			if _, err := s.store.CancelRepository(ctx, repo.Name, "GitHub repository authorization: "+state); err != nil {
				return nil, err
			}
		}
	}
	return allowed, nil
}

func installationForRepository(repo RepositoryConfig, snapshot *GitHubAuthorization) int64 {
	if repo.InstallationID != 0 {
		return repo.InstallationID
	}
	if snapshot == nil {
		return 0
	}
	if snapshot.Catalog != nil {
		for _, remote := range snapshot.Catalog.Repositories {
			if repo.RepositoryID > 0 && repo.RepositoryID == remote.ID || repo.RepositoryID == 0 && strings.EqualFold(remote.Name, repo.Name) {
				return remote.InstallationID
			}
		}
	}
	return snapshot.RepositoryInstallations[strings.ToLower(repo.Name)]
}
