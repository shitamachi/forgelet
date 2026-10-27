package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/shitamachi/forgelet/internal/delivery"
	"github.com/shitamachi/forgelet/internal/run"
	"github.com/shitamachi/forgelet/internal/workflow"
)

type RepositoryConfig struct {
	Name           string `json:"name"`
	RepositoryID   int64  `json:"repository_id,omitempty"`
	InstallationID int64  `json:"installation_id"`
	Enabled        bool   `json:"enabled"`
}
type WorkflowConfig struct {
	Repository string `json:"repository"`
	Path       string `json:"path"`
	Enabled    bool   `json:"enabled"`
	Source     string `json:"source"`
}
type Management struct {
	Version      int                `json:"version"`
	Repositories []RepositoryConfig `json:"repositories"`
	Workflows    []WorkflowConfig   `json:"workflows"`
	Targets      []delivery.Target  `json:"targets"`
}

func (s *Server) management() Management {
	if p := s.managed.Load(); p != nil {
		return *p
	}
	m := Management{Repositories: []RepositoryConfig{}, Workflows: []WorkflowConfig{}, Targets: []delivery.Target{}}
	for _, r := range s.cfg.Repositories {
		m.Repositories = append(m.Repositories, RepositoryConfig{Name: r, Enabled: true})
		for _, p := range s.cfg.Workflows {
			m.Workflows = append(m.Workflows, WorkflowConfig{Repository: r, Path: p, Enabled: true})
		}
	}
	for _, t := range s.cfg.Targets {
		m.Targets = append(m.Targets, t)
	}
	slices.SortFunc(m.Targets, func(a, b delivery.Target) int { return strings.Compare(a.Name, b.Name) })
	return m
}
func (s *Server) LoadManagement(ctx context.Context) error {
	body, err := s.store.Management(ctx)
	if err != nil {
		return fmt.Errorf("load management: %w", err)
	}
	if len(body) == 0 {
		return nil
	}
	var m Management
	if err = json.Unmarshal(body, &m); err != nil {
		return fmt.Errorf("decode management: %w", err)
	}
	if err = s.validateManagement(m, false); err != nil {
		return fmt.Errorf("stored management: %w", err)
	}
	s.managed.Store(&m)
	return nil
}
func configuredRepository(name string) bool {
	if !repositoryPattern.MatchString(name) {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	return true
}
func workflowPath(p string) bool {
	return strings.HasPrefix(p, ".github/workflows/") && path.Dir(p) == ".github/workflows" && (strings.HasSuffix(p, ".yml") || strings.HasSuffix(p, ".yaml")) && !strings.ContainsAny(p, "\\\x00\r\n")
}
func (s *Server) validateManagement(m Management, requireRuntime bool) error {
	if m.Version < 0 || len(m.Repositories) > 128 || len(m.Workflows) > 256 || len(m.Targets) > 32 {
		return errors.New("configuration limit exceeded")
	}
	repos := map[string]bool{}
	seen := map[string]bool{}
	for _, r := range m.Repositories {
		if !configuredRepository(r.Name) || r.RepositoryID < 0 || r.RepositoryID > 9007199254740991 || r.InstallationID < 0 || r.InstallationID > 9007199254740991 || repos[r.Name] {
			return errors.New("invalid or duplicate repository")
		}
		repos[r.Name] = true
	}
	for _, w := range m.Workflows {
		key := w.Repository + ":" + w.Path
		if !repos[w.Repository] || !workflowPath(w.Path) || seen[key] || len(w.Source) > 256<<10 {
			return errors.New("invalid, duplicate or oversized workflow")
		}
		seen[key] = true
		if w.Source != "" {
			var root struct {
				Jobs map[string]any `yaml:"jobs"`
			}
			if err := yaml.Unmarshal([]byte(w.Source), &root); err != nil || len(root.Jobs) == 0 {
				return errors.New("workflow requires valid YAML and jobs")
			}
			plans, err := workflow.ValidateFiles([]byte(w.Source), run.Event{Repository: w.Repository, WorkflowPath: w.Path, Name: "push", Ref: "refs/heads/main", SHA: strings.Repeat("a", 40)}, managementSources(m.Workflows, w.Repository))
			if err != nil {
				return fmt.Errorf("workflow %s: %w", w.Path, err)
			}
			if requireRuntime {
				if err := s.validateProfiles(plans); err != nil {
					return err
				}
			}

		}
	}
	names := map[string]bool{}
	for _, t := range m.Targets {
		if !repos[t.SourceRepository] || !configuredRepository(t.Repository) || names[t.Name] {
			return errors.New("target requires a registered source repository and unique name")
		}
		names[t.Name] = true
		if err := t.Validate(); err != nil {
			return err
		}
		if requireRuntime && s.cfg.Delivery == nil {
			return errors.New("GitOps requires configured GitHub App and Flux adapter")
		}
	}
	return nil
}
func (s *Server) managementAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, 200, s.management())
		return
	}
	var next Management
	if err := decode(w, r, &next, 4<<20); err != nil {
		http.Error(w, "invalid management document", 400)
		return
	}
	if err := s.validateManagement(next, true); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.tickMu.Lock()
	defer s.tickMu.Unlock()
	current := s.management()
	if next.Version != current.Version {
		http.Error(w, "configuration changed; reload before saving", http.StatusConflict)
		return
	}
	old, err := s.store.Management(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	next.Version++
	body, err := json.Marshal(next)
	if err != nil {
		serverError(w, err)
		return
	}
	if err = s.store.SaveManagement(r.Context(), old, body); err != nil {
		serverError(w, err)
		return
	}
	s.managed.Store(&next)
	writeJSON(w, 200, next)
}
func (s *Server) target(name string) (delivery.Target, bool) {
	for _, t := range s.management().Targets {
		if t.Name == name {
			return t, true
		}
	}
	return delivery.Target{}, false
}
func (s *Server) workflowFiles(repo string, files map[string][]byte) map[string][]byte {
	rules := []WorkflowConfig{}
	for _, w := range s.management().Workflows {
		if w.Repository == repo {
			rules = append(rules, w)
		}
	}
	if len(rules) == 0 {
		return files
	}
	result := map[string][]byte{}
	for _, w := range rules {
		if !w.Enabled {
			continue
		}
		if w.Source != "" {
			result[w.Path] = []byte(w.Source)
		} else if b, ok := files[w.Path]; ok {
			result[w.Path] = b
		}
	}
	return result
}
func (s *Server) inspectWorkflows(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Repository string `json:"repository"`
		SHA        string `json:"sha"`
	}
	if err := decode(w, r, &input, 4096); err != nil || !s.allowed(input.Repository) || !shaPattern.MatchString(input.SHA) {
		http.Error(w, "registered repository and full SHA required", 400)
		return
	}
	if s.provider == nil {
		http.Error(w, "GitHub App is not configured", http.StatusServiceUnavailable)
		return
	}
	var installation int64
	for _, repo := range s.management().Repositories {
		if repo.Name == input.Repository {
			installation = repo.InstallationID
		}
	}
	if installation <= 0 {
		http.Error(w, "repository installation ID is required", 400)
		return
	}
	files, err := s.provider.Workflows(r.Context(), run.Event{Repository: input.Repository, SHA: strings.ToLower(input.SHA), InstallationID: installation})
	if err != nil {
		http.Error(w, "cannot read workflows with this GitHub App installation", http.StatusBadGateway)
		return
	}
	result := []WorkflowConfig{}
	for p, b := range files {
		result = append(result, WorkflowConfig{Repository: input.Repository, Path: p, Source: string(b), Enabled: true})
	}
	slices.SortFunc(result, func(a, b WorkflowConfig) int { return strings.Compare(a.Path, b.Path) })
	writeJSON(w, 200, result)
}

func (s *Server) eventWorkflows(ctx context.Context, e run.Event) (map[string][]byte, error) {
	rules := false
	needsGit := false
	for _, w := range s.management().Workflows {
		if w.Repository == e.Repository {
			rules = true
			if w.Enabled && w.Source == "" {
				needsGit = true
			}
		}
	}
	if rules && !needsGit {
		return s.workflowFiles(e.Repository, nil), nil
	}
	return s.provider.Workflows(ctx, e)
}

func managementSources(workflows []WorkflowConfig, repository string) map[string][]byte {
	files := map[string][]byte{}
	for _, w := range workflows {
		if w.Repository == repository && w.Source != "" {
			files[w.Path] = []byte(w.Source)
		}
	}
	return files
}
func (s *Server) sourceBundle(repository string, git map[string][]byte) map[string][]byte {
	files := map[string][]byte{}
	for path, data := range git {
		files[path] = data
	}
	for path, data := range managementSources(s.management().Workflows, repository) {
		files[path] = data
	}
	return files
}
