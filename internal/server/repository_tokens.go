package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/shitamachi/forgelet/internal/run"
)

// RepositoryTokens maps a source repository's secret name to one explicitly
// allowed private dependency repository. Values are short-lived read tokens,
// minted when a claimed job requests its credentials, never persisted secrets.
type RepositoryTokens map[string]map[string]string

func validateRepositoryTokens(tokens RepositoryTokens) error {
	if len(tokens) > 100 {
		return fmt.Errorf("too many repository token bindings")
	}
	for source, bindings := range tokens {
		if !configuredRepository(source) || len(bindings) > 25 {
			return fmt.Errorf("invalid repository token bindings")
		}
		for name, target := range bindings {
			if !settingName.MatchString(name) || strings.HasPrefix(name, "GITHUB_") || strings.HasPrefix(name, "FORGELET_") || !configuredRepository(target) {
				return fmt.Errorf("invalid repository token reference")
			}
		}
	}
	return nil
}
func (s *Server) dependencyToken(ctx context.Context, plan run.Plan, target string) (string, error) {
	item, err := s.store.GetRun(ctx, plan.RunID)
	if err != nil {
		return "", err
	}
	if item.InstallationID == 0 || s.provider == nil {
		return "", fmt.Errorf("repository token requires a GitHub App installation")
	}
	token, err := s.provider.RepositoryToken(ctx, item.InstallationID, target)
	if err != nil {
		return "", fmt.Errorf("obtain scoped dependency repository token: %w", err)
	}
	return token, nil
}
