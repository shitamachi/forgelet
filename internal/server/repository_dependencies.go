package server

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/shitamachi/forgelet/internal/run"
)

func normalizeRepositoryDependencies(config map[string][]string) (map[string][]string, error) {
	if len(config) > 100 {
		return nil, fmt.Errorf("too many repository dependency policies")
	}
	result := map[string][]string{}
	for source, dependencies := range config {
		if !configuredRepository(source) || len(dependencies) > 16 {
			return nil, fmt.Errorf("invalid repository dependency policy")
		}
		owner, _, _ := strings.Cut(source, "/")
		for _, dependency := range dependencies {
			dependencyOwner, _, _ := strings.Cut(dependency, "/")
			if !configuredRepository(dependency) || !strings.EqualFold(owner, dependencyOwner) {
				return nil, fmt.Errorf("repository dependency must belong to the source installation owner")
			}
			if !strings.EqualFold(source, dependency) {
				result[source] = append(result[source], strings.ToLower(dependency))
			}
		}
		slices.Sort(result[source])
		result[source] = slices.Compact(result[source])
	}
	return result, nil
}

func (s *Server) repositoryDependencyAdmission(p *run.Plan) error {
	allowed := s.cfg.RepositoryDependencies[p.Repository]
	if (p.RepositoryDependenciesSelected || len(p.RepositoryDependencies) > 0) && !slices.Equal(p.RepositoryDependencies, allowed) {
		return fmt.Errorf("saved repository dependency policy changed; create a new run")
	}
	if len(allowed) > 0 && p.Permissions != nil && p.Permissions["contents"] != "read" {
		return fmt.Errorf("private repository dependencies require contents: read")
	}
	p.RepositoryDependencies = slices.Clone(allowed)
	p.RepositoryDependenciesSelected = true
	return nil
}

func (s *Server) requireSourceDependencies(p run.Plan, installation int64) error {
	if !slices.Equal(p.RepositoryDependencies, s.cfg.RepositoryDependencies[p.Repository]) {
		return fmt.Errorf("repository dependency policy changed")
	}
	if _, discovered := s.provider.(onboardingProvider); !discovered {
		return nil
	}
	for _, dependency := range p.RepositoryDependencies {
		state := repositoryAuthorization(s.authorization.Load(), RepositoryConfig{Name: dependency, InstallationID: installation}, installation).State
		if state != "authorized" {
			return fmt.Errorf("dependency repository %s authorization: %s", dependency, state)
		}
	}
	return nil
}

func (s *Server) sourceToken(ctx context.Context, p run.Plan, installation int64) (string, error) {
	if err := s.requireSourceDependencies(p, installation); err != nil {
		return "", err
	}
	if len(p.RepositoryDependencies) == 0 {
		return s.provider.RepositoryToken(ctx, installation, p.Repository)
	}
	provider, ok := s.provider.(interface {
		SourceToken(context.Context, int64, string, []string) (string, error)
	})
	if !ok {
		return "", fmt.Errorf("provider does not support scoped source dependencies")
	}
	return provider.SourceToken(ctx, installation, p.Repository, p.RepositoryDependencies)
}
