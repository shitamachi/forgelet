package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	gh "github.com/shitamachi/forgelet/internal/provider/github"
	"github.com/shitamachi/forgelet/internal/run"
	"github.com/shitamachi/forgelet/internal/storage"
	"github.com/shitamachi/forgelet/internal/workflow"
)

type onboardingProvider interface {
	Discover(context.Context) (gh.Catalog, error)
	DefaultCommit(context.Context, gh.Repository) (string, error)
}

func (s *Server) catalog(ctx context.Context) (gh.Catalog, error) {
	p, ok := s.provider.(onboardingProvider)
	if !ok {
		return gh.Catalog{}, errors.New("GitHub App discovery is not configured")
	}
	return p.Discover(ctx)
}
func (s *Server) githubConnection(w http.ResponseWriter, r *http.Request) {
	catalog, err := s.catalog(r.Context())
	if err != nil {
		http.Error(w, "Cannot refresh GitHub authorization. Check App credentials, API availability and installation permissions; previous configuration is unchanged.", http.StatusBadGateway)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, catalog)
}

type onboardingInput struct {
	RepositoryID int64          `json:"repository_id"`
	Version      int            `json:"version"`
	SHA          string         `json:"sha"`
	Path         string         `json:"path"`
	Ref          string         `json:"ref"`
	EventName    string         `json:"event_name"`
	Inputs       map[string]any `json:"inputs"`
	RequestID    string         `json:"request_id"`
}

func (s *Server) authorizedRepository(ctx context.Context, id int64) (gh.Repository, error) {
	catalog, err := s.catalog(ctx)
	if err != nil {
		return gh.Repository{}, fmt.Errorf("refresh GitHub authorization failed: %w", err)
	}
	for _, repo := range catalog.Repositories {
		if repo.ID == id {
			if repo.Archived || repo.Disabled {
				return gh.Repository{}, errors.New("repository is archived or disabled")
			}
			for _, inst := range catalog.Installations {
				if inst.ID == repo.InstallationID && inst.SuspendedAt == nil && (inst.Permissions["contents"] == "read" || inst.Permissions["contents"] == "write") && inst.Permissions["checks"] == "write" {
					return repo, nil
				}
			}
			return gh.Repository{}, errors.New("installation requires Contents read and Checks write permissions")
		}
	}
	return gh.Repository{}, errors.New("repository is not authorized for this GitHub App; authorize and refresh")
}
func (s *Server) githubEnable(w http.ResponseWriter, r *http.Request) {
	var input onboardingInput
	if err := decode(w, r, &input, 16384); err != nil || input.RepositoryID <= 0 {
		http.Error(w, "repository_id required", http.StatusBadRequest)
		return
	}
	repo, err := s.authorizedRepository(r.Context(), input.RepositoryID)
	if err != nil {
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
func (s *Server) enableRepository(ctx context.Context, repo gh.Repository, version int) (Management, error) {
	s.tickMu.Lock()
	defer s.tickMu.Unlock()
	// Clone slices: readers retain the previous immutable configuration snapshot.
	current := s.management()
	body, err := json.Marshal(current)
	if err != nil {
		return Management{}, err
	}
	var next Management
	if err = json.Unmarshal(body, &next); err != nil {
		return Management{}, err
	}
	if version != next.Version {
		return Management{}, fmt.Errorf("configuration changed; refresh before enabling: %w", storage.ErrConflict)
	}
	found := false
	for i, old := range next.Repositories {
		if old.RepositoryID == repo.ID && old.Name != repo.Name {
			return Management{}, fmt.Errorf("repository renamed; update workflow and target bindings explicitly: %w", storage.ErrConflict)
		}
		if strings.EqualFold(old.Name, repo.Name) {
			if old.RepositoryID != 0 && old.RepositoryID != repo.ID {
				return Management{}, fmt.Errorf("repository name now belongs to a different GitHub repository ID; remove the old registration explicitly before reconnecting: %w", storage.ErrConflict)
			}

			next.Repositories[i] = RepositoryConfig{Name: repo.Name, RepositoryID: repo.ID, InstallationID: repo.InstallationID, Enabled: true}
			found = true
		}
	}
	if !found {
		next.Repositories = append(next.Repositories, RepositoryConfig{Name: repo.Name, RepositoryID: repo.ID, InstallationID: repo.InstallationID, Enabled: true})
	}
	if err = s.validateManagement(next, true); err != nil {
		return Management{}, fmt.Errorf("validate repository configuration: %w", err)
	}
	old, err := s.store.Management(ctx)
	if err != nil {
		return Management{}, err
	}
	next.Version++
	body, err = json.Marshal(next)
	if err != nil {
		return Management{}, err
	}
	if err = s.store.SaveManagement(ctx, old, body); err != nil {
		return Management{}, err
	}
	s.managed.Store(&next)
	return next, nil
}

type workflowPreview struct {
	Path       string `json:"path"`
	Source     string `json:"source"`
	Diagnostic string `json:"diagnostic"`
	Runnable   bool   `json:"runnable"`
	Jobs       int    `json:"jobs"`
}
type onboardingPreview struct {
	Repository gh.Repository     `json:"repository"`
	SHA        string            `json:"sha"`
	Ref        string            `json:"ref"`
	Workflows  []workflowPreview `json:"workflows"`
}

func (s *Server) onboardingFiles(ctx context.Context, input onboardingInput) (onboardingPreview, run.Event, map[string][]byte, error) {
	repo, err := s.authorizedRepository(ctx, input.RepositoryID)
	if err != nil {
		return onboardingPreview{}, run.Event{}, nil, err
	}
	bound := false
	for _, registered := range s.management().Repositories {
		if registered.Name == repo.Name && registered.Enabled && registered.InstallationID == repo.InstallationID && registered.RepositoryID == repo.ID {
			bound = true
		}
	}
	if !bound {
		return onboardingPreview{}, run.Event{}, nil, errors.New("enable this authorized repository before checking workflows")
	}
	sha := strings.ToLower(input.SHA)
	ref := input.Ref
	if sha == "" {
		sha, err = s.provider.(onboardingProvider).DefaultCommit(ctx, repo)
		if err != nil {
			return onboardingPreview{}, run.Event{}, nil, err
		}
		ref = "refs/heads/" + repo.DefaultBranch
	}
	if !shaPattern.MatchString(sha) || !strings.HasPrefix(ref, "refs/heads/") || len(ref) > 1024 || strings.ContainsAny(ref, "\x00\r\n") {
		return onboardingPreview{}, run.Event{}, nil, errors.New("valid commit SHA and branch ref required")
	}
	name := defaultString(input.EventName, "push")
	if name != "push" && name != "workflow_dispatch" {
		return onboardingPreview{}, run.Event{}, nil, errors.New("event must be push or workflow_dispatch")
	}
	event := run.Event{Repository: repo.Name, InstallationID: repo.InstallationID, SHA: sha, Ref: ref, Name: name, Inputs: input.Inputs, Payload: json.RawMessage(`{}`)}
	files, err := s.eventWorkflows(ctx, event)
	if err != nil {
		return onboardingPreview{}, event, nil, fmt.Errorf("read fixed commit workflows: %w", err)
	}
	files = s.workflowFiles(repo.Name, files)
	return onboardingPreview{Repository: repo, SHA: sha, Ref: ref, Workflows: []workflowPreview{}}, event, files, nil
}
func (s *Server) checkOnboarding(source []byte, event run.Event, path string) (int, error) {
	event.WorkflowPath = path
	plans, err := workflow.Validate(source, event)
	if err != nil {
		return 0, err
	}
	for _, plan := range plans {
		if _, ok := s.cfg.Profiles[plan.Profile]; !ok {
			return 0, fmt.Errorf("unknown execution profile %s; select a configured runs-on profile", plan.Profile)
		}
	}
	plans, err = workflow.Compile(source, event, "onboarding-preview")
	if err != nil {
		return 0, err
	}
	if len(plans) == 0 {
		return 0, errors.New("workflow has no executable jobs")
	}
	return len(plans), nil
}
func (s *Server) githubPreview(w http.ResponseWriter, r *http.Request) {
	var input onboardingInput
	if err := decode(w, r, &input, 16384); err != nil {
		http.Error(w, "invalid preview request", http.StatusBadRequest)
		return
	}
	preview, event, files, err := s.onboardingFiles(r.Context(), input)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	for path, source := range files {
		jobs, err := s.checkOnboarding(source, event, path)
		item := workflowPreview{Path: path, Source: string(source), Runnable: err == nil, Jobs: jobs}
		if err != nil {
			item.Diagnostic = err.Error()
		}
		preview.Workflows = append(preview.Workflows, item)
	}
	slices.SortFunc(preview.Workflows, func(a, b workflowPreview) int { return strings.Compare(a.Path, b.Path) })
	writeJSON(w, 200, preview)
}
func (s *Server) githubRun(w http.ResponseWriter, r *http.Request) {
	var input onboardingInput
	if err := decode(w, r, &input, 16384); err != nil || input.SHA == "" || !workflowPath(input.Path) || !identifierRequest(input.RequestID) {
		http.Error(w, "fixed SHA, workflow path and unique request_id required", http.StatusBadRequest)
		return
	}
	version := s.management().Version
	_, event, files, err := s.onboardingFiles(r.Context(), input)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	source, ok := files[input.Path]
	if !ok {
		http.Error(w, "enabled workflow is missing at this commit", http.StatusUnprocessableEntity)
		return
	}
	if _, err = s.checkOnboarding(source, event, input.Path); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	s.tickMu.Lock()
	defer s.tickMu.Unlock()
	if s.management().Version != version || !s.allowed(event.Repository) {
		http.Error(w, "repository is disabled", http.StatusConflict)
		return
	}
	inputBytes, err := json.Marshal(input.Inputs)
	if err != nil {
		serverError(w, err)
		return
	}
	event.DeliveryID = "onboarding-" + stableID(event.Repository, event.SHA, input.Path, input.Ref, input.EventName, string(inputBytes), string(source), input.RequestID)
	item, err := s.createRun(r.Context(), event, input.Path, source)
	if err != nil {
		if errors.Is(err, ErrWorkflow) {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		} else {
			serverError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusCreated, item)
}
func identifierRequest(s string) bool {
	if len(s) < 16 || len(s) > 80 {
		return false
	}
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}
