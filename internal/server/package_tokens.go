package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/shitamachi/forgelet/internal/run"
)

// PackageTokens binds a repository to an encrypted setting name, never to a
// credential value. A source installation token is never granted packages:write.
func validatePackageTokens(bindings map[string]string) error {
	if len(bindings) > 100 {
		return fmt.Errorf("too many package token bindings")
	}
	for repository, name := range bindings {
		if !configuredRepository(repository) || !settingName.MatchString(name) || strings.HasPrefix(name, "GITHUB_") || strings.HasPrefix(name, "FORGELET_") {
			return fmt.Errorf("invalid package token secret reference")
		}
	}
	return nil
}
func (s *Server) packageAdmission(p *run.Plan, act bool) error {
	permission := p.Permissions["packages"]
	if permission == "" || permission == "none" {
		return nil
	}
	if permission != "write" {
		return fmt.Errorf("only explicit packages:write publication is supported")
	}
	if !act {
		return fmt.Errorf("packages permission requires an act profile")
	}
	if p.EventName != "push" && p.EventName != "workflow_dispatch" {
		return fmt.Errorf("package credentials require push or workflow_dispatch; PR publication is forbidden")
	}
	reference := s.cfg.PackageTokens[p.Repository]
	if p.PackageTokenSecret != "" && reference != p.PackageTokenSecret {
		return fmt.Errorf("saved package credential binding changed; restore it before rerun")
	}
	if reference == "" {
		return fmt.Errorf("packages:%s requires an administrator package_tokens secret binding; source GITHUB_TOKEN remains read-only", permission)
	}
	p.PackageTokenSecret = reference
	return nil
}
func (s *Server) packageToken(ctx context.Context, p run.Plan) (string, error) {
	if p.PackageTokenSecret == "" {
		return "", nil
	}
	if p.Kernel != "act" || (p.EventName != "push" && p.EventName != "workflow_dispatch") || p.Permissions["packages"] != "write" || s.cfg.PackageTokens[p.Repository] != p.PackageTokenSecret {
		return "", fmt.Errorf("package credential capability revoked")
	}
	settings, err := s.store.Settings(ctx, p.Repository, "secrets")
	if err != nil {
		return "", err
	}
	data := settings[p.PackageTokenSecret]
	if len(data) == 0 {
		return "", fmt.Errorf("package credential is not configured")
	}
	token, err := openSecret(s.cfg.SecretKey, p.Repository, p.PackageTokenSecret, data)
	if err != nil {
		return "", err
	}
	if token == "" {
		return "", fmt.Errorf("package credential is empty")
	}
	return token, nil
}
