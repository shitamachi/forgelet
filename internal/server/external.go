package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/shitamachi/forgelet/internal/kube"
	"github.com/shitamachi/forgelet/internal/run"
	"github.com/shitamachi/forgelet/internal/storage"
)

type ExternalRunner struct {
	// Legacy registrations supply workflow aliases and a fallback build policy.
	Build       *run.ExternalBuild `yaml:"build,omitempty" json:"build,omitempty"`
	Repository  string             `yaml:"repository,omitempty" json:"repository,omitempty"`
	Workflow    string             `yaml:"workflow,omitempty" json:"workflow,omitempty"`
	Ref         string             `yaml:"ref,omitempty" json:"ref,omitempty"`
	TokenSHA256 string             `yaml:"token_sha256" json:"-"`
}

type ExternalBuildPolicy struct {
	Repository string            `yaml:"repository"`
	Workflow   string            `yaml:"workflow"`
	Ref        string            `yaml:"ref"`
	Build      run.ExternalBuild `yaml:"build"`
}

type externalStore interface {
	ReserveExternal(context.Context, int, storage.ExternalReservation) (run.Job, error)
	RenewExternal(context.Context, string, string) (run.Job, error)
	ExpireExternal(context.Context, string, time.Time) error
	RunnerRegistrations(context.Context) ([]byte, error)
	SaveRunnerRegistrations(context.Context, []byte) error
}

var runnerName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

func validExternalSource(repository, workflow, ref string) bool {
	return configuredRepository(repository) && workflowPath(workflow) &&
		strings.HasPrefix(ref, "refs/heads/") && len(ref) > len("refs/heads/") &&
		!strings.ContainsAny(ref, " ~^:?*[\\\x00\r\n") && !strings.Contains(ref, "..")
}

func validateExternalRunners(cfg Config, store externalStore) error {
	seen := map[string]bool{}
	for name, runner := range cfg.ExternalRunners {
		_, pod := cfg.Profiles[name]
		digest, err := hex.DecodeString(runner.TokenSHA256)
		key := strings.ToLower(runner.TokenSHA256)
		legacy := runner.Repository != "" || runner.Workflow != "" || runner.Ref != "" || runner.Build != nil
		if store == nil || seen[key] || !runnerName.MatchString(name) || err != nil || len(digest) != sha256.Size ||
			legacy && (pod || !validExternalSource(runner.Repository, runner.Workflow, runner.Ref) ||
				runner.Build != nil && !runner.Build.Valid()) {
			return fmt.Errorf("invalid external runner %q", name)
		}
		seen[key] = true
	}
	return nil
}

func validateExternalBuilds(policies []ExternalBuildPolicy) error {
	seen := map[[3]string]bool{}
	for _, p := range policies {
		key := [3]string{strings.ToLower(p.Repository), p.Workflow, p.Ref}
		if seen[key] || !validExternalSource(p.Repository, p.Workflow, p.Ref) || !p.Build.Valid() {
			return fmt.Errorf("invalid or duplicate external build policy for %q %q %q", p.Repository, p.Workflow, p.Ref)
		}
		seen[key] = true
	}
	return nil
}

func (s *Server) externalBuild(p run.Plan, legacy map[string]ExternalRunner) (*run.ExternalBuild, error) {
	for _, policy := range s.cfg.ExternalBuilds {
		if strings.EqualFold(p.Repository, policy.Repository) && p.SourceWorkflow == policy.Workflow && p.Ref == policy.Ref {
			return &policy.Build, nil
		}
	}
	if runner, ok := legacy[p.Profile]; ok && runner.Build != nil && runner.permitsLegacy(p) {
		return runner.Build, nil
	}
	var build *run.ExternalBuild
	for _, runner := range legacy {
		if runner.Build == nil || !runner.permitsLegacy(p) {
			continue
		}
		if build != nil && *build != *runner.Build {
			return nil, fmt.Errorf("conflicting legacy external builds; configure external_builds for %q", p.SourceWorkflow)
		}
		build = runner.Build
	}
	return build, nil
}

func (s *Server) validateExternalPlans(plans []run.Plan) error {
	s.runnerMu.Lock()
	legacy := s.runnerPoliciesLocked()
	s.runnerMu.Unlock()
	hasExternal, hasExternalOnly := false, false
	for i := range plans {
		p := &plans[i]
		runner := legacy[p.Profile]
		_, pod := s.cfg.Profiles[p.Profile]
		fallback := s.cfg.RunnerFallbacks[p.Profile]
		if s.external == nil || fallback == "" && (pod || p.Profile != run.MacRunnerLabel && runner.Workflow == "") {
			if p.External {
				return fmt.Errorf("external runner label %q is no longer available", p.Profile)
			}
			continue
		}
		if p.EventName != "push" && p.EventName != "workflow_dispatch" || !strings.HasPrefix(p.Ref, "refs/heads/") ||
			p.Profile != run.MacRunnerLabel && runner.Workflow != "" && !runner.permitsLegacy(*p) {
			return fmt.Errorf("external runner label %q requires an authorized branch push or manual run", p.Profile)
		}
		if p.Control != "" || p.Kernel == "act" || p.ParentGate != "" || len(p.SecretNames) > 0 ||
			len(p.SecretBindings) > 0 || len(p.RequiredSecrets) > 0 || p.PackageTokenSecret != "" ||
			p.Permissions["packages"] != "" || p.ImageMirrorsSelected {
			return fmt.Errorf("external profile %q requires a standalone native workflow without secrets or package permissions", p.Profile)
		}
		var buildStep string
		var build *run.ExternalBuild
		for _, step := range p.Steps {
			if step.Action != nil && step.Action.Uses == "forgelet/build@v1" {
				var err error
				build, err = s.externalBuild(*p, legacy)
				if err != nil {
					return err
				}
				if build == nil || buildStep != "" || step.Action.Inputs["image"] != build.Image {
					return fmt.Errorf("external runner label %q requires one build step with its configured image repository", p.Profile)
				}
				buildStep = step.ID
				continue
			}
			if step.Shell == "pwsh" || step.Action != nil &&
				step.Action.Uses != "actions/upload-artifact@v4" && step.Action.Uses != "actions/download-artifact@v4" {
				return fmt.Errorf("external profile %q supports bash, sh, native checkout, artifacts and configured image builds", p.Profile)
			}
		}
		if buildStep != "" {
			if p.ExternalBuild != nil && *p.ExternalBuild != *build {
				return fmt.Errorf("external build policy changed; create a new run")
			}
			value := fmt.Sprintf("${{ steps['%s'].outputs.digest }}", buildStep)
			old, exists := p.Outputs[run.ExternalImageDigestOutput]
			if exists && old != value || !exists && len(p.Outputs) >= 100 {
				return fmt.Errorf("external build output %q conflicts with workflow", run.ExternalImageDigestOutput)
			}
			if p.Outputs == nil {
				p.Outputs = map[string]string{}
			}
			p.Outputs[run.ExternalImageDigestOutput] = value
			snapshot := *build
			p.ExternalBuild = &snapshot
		}
		p.FallbackProfile = fallback
		if fallback != "" {
			if _, err := s.fallbackPodProfile(*p); err != nil {
				return err
			}
		}
		p.External = true
		hasExternal = true
		hasExternalOnly = hasExternalOnly || fallback == ""
	}
	if !hasExternal {
		return nil
	}
	for _, p := range plans {
		if p.External {
			continue
		}
		if p.AutomaticDeployment == "" {
			if !hasExternalOnly {
				continue
			}
			return fmt.Errorf("external workflows can only share a run with controller-generated deployment jobs")
		}
		target, ok := s.target(p.AutomaticDeployment)
		if !ok || target.OnSuccess == nil || target.OnSuccess.Profile != p.Profile || p.ExternalDeploymentImage != target.Image ||
			target.SourceRepository != p.Repository || target.SourceRef != p.Ref || target.OnSuccess.WorkflowPath != p.SourceWorkflow {
			return fmt.Errorf("external deployment target %q has no matching automatic policy", p.AutomaticDeployment)
		}
		var validPublisher bool
		for _, publisher := range plans {
			if publisher.Key == target.OnSuccess.Job && publisher.ExternalBuild != nil && publisher.ExternalBuild.Image == target.Image {
				validPublisher = true
				break
			}
		}
		if !validPublisher {
			return fmt.Errorf("external deployment target %q requires a publisher for image %q", p.AutomaticDeployment, target.Image)
		}
	}
	return nil
}

func (r ExternalRunner) permitsLegacy(p run.Plan) bool {
	return strings.EqualFold(p.Repository, r.Repository) && p.SourceWorkflow == r.Workflow && p.Ref == r.Ref
}

type externalJobRoute struct{}

func (s *Server) runnerRoutes(r chi.Router) {
	if s.external == nil {
		return
	}
	r.Route("/runner/v1", func(r chi.Router) {
		r.Post("/poll", s.runnerPoll)
		r.Post("/renew", s.runnerRenew)
		r.Route("/jobs/{jobID}", func(r chi.Router) {
			r.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), externalJobRoute{}, true)))
				})
			})
			r.Post("/claim", s.claim)
			r.Get("/credentials", s.credentials)
			r.Get("/control", s.control)
			r.Post("/logs", s.appendLog)
			r.Post("/progress", s.appendProgress)
			r.Post("/result", s.result)
			r.Get("/blobs/{kind}", s.runtimeBlob)
			r.Put("/blobs/{kind}", s.runtimeBlob)
		})
	})
}

func (s *Server) runnerMachine(r *http.Request) string {
	token, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer frr_")
	if !bearer || len(token) != 64 {
		return ""
	}
	got := sha256.Sum256([]byte("frr_" + token))
	for name, runner := range s.runnerPoliciesLocked() {
		expected, _ := hex.DecodeString(runner.TokenSHA256)
		if subtle.ConstantTimeCompare(expected, got[:]) == 1 {
			return name
		}
	}
	return ""
}

type runnerRequest struct {
	Profile string `json:"profile,omitempty"` // Ignored; accepted for old clients.
	Attempt string `json:"attempt"`
	JobID   string `json:"job_id,omitempty"`
}

func (s *Server) runnerPoll(w http.ResponseWriter, r *http.Request) {
	var input runnerRequest
	if decode(w, r, &input, 2048) != nil {
		http.Error(w, "invalid runner request", http.StatusBadRequest)
		return
	}
	s.runnerMu.Lock()
	defer s.runnerMu.Unlock()
	runnerID := s.runnerMachine(r)
	attempt := run.ExternalAttempt(input.Attempt)
	if runnerID == "" || attempt == "" {
		http.Error(w, "invalid runner identity", http.StatusUnauthorized)
		return
	}
	if err := s.disk.check(r.Context(), s.now()); err != nil {
		serverError(w, err)
		return
	}
	limit := s.cfg.MaxParallel
	if s.disk.snapshot().Paused || s.runners[runnerID].Paused {
		limit = 0
	}
	repositories := map[string]bool{}
	for _, repo := range s.management().Repositories {
		if repo.Enabled && s.requireRepositoryAccess(repo.Name, 0) == nil {
			repositories[strings.ToLower(repo.Name)] = true
		}
	}
	platforms := runnerPlatforms(r.Header.Get(run.RunnerBuildPlatformsHeader))
	job, err := s.external.ReserveExternal(r.Context(), limit, storage.ExternalReservation{
		RunnerID: runnerID, AttemptID: attempt, Repositories: repositories,
		PodFirst: !s.preferRunner(), Platforms: platforms,
	})
	if err != nil {
		serverError(w, err)
		return
	}
	now := s.now().UTC()
	s.runnerSeen[runnerID] = now
	if job.ID == "" {
		s.runnerReady[runnerID] = runnerAvailability{at: now, platforms: platforms}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	delete(s.runnerReady, runnerID)
	grant := run.ExternalGrant{JobID: job.ID, RunID: job.RunID, Name: job.Name,
		Repository: job.Plan.Repository, SHA: job.Plan.SHA, Seconds: leaseSeconds(job)}
	if job.Plan.ExternalBuild != nil {
		grant.Platform = job.Plan.ExternalBuild.Platform
	}
	writeJSON(w, http.StatusOK, grant)
}

func (s *Server) runnerRenew(w http.ResponseWriter, r *http.Request) {
	var input runnerRequest
	if decode(w, r, &input, 2048) != nil {
		http.Error(w, "invalid runner request", http.StatusBadRequest)
		return
	}
	s.runnerMu.Lock()
	defer s.runnerMu.Unlock()
	runnerID := s.runnerMachine(r)
	if runnerID == "" {
		http.Error(w, "invalid runner identity", http.StatusUnauthorized)
		return
	}
	job, err := s.store.GetJob(r.Context(), input.JobID)
	if err != nil {
		serverError(w, err)
		return
	}
	if !job.Plan.External || job.ExternalRunnerID() != runnerID || !s.allowed(job.Plan.Repository) {
		http.Error(w, "runner authorization expired", http.StatusForbidden)
		return
	}
	if err := s.requireRepositoryAccess(job.Plan.Repository, 0); err != nil {
		serverError(w, err)
		return
	}
	job, err = s.external.RenewExternal(r.Context(), job.ID, run.ExternalAttempt(input.Attempt))
	if err != nil {
		serverError(w, err)
		return
	}
	s.runnerSeen[runnerID] = s.now().UTC()
	writeJSON(w, http.StatusOK, map[string]any{"cancel": job.CancelRequested, "terminal": job.State.Terminal(), "lease_seconds": leaseSeconds(job)})
}

func (s *Server) authenticateExternal(r *http.Request) (run.Job, executionIdentity, error) {
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	attempt := run.ExternalAttempt(token)
	if attempt == "" {
		return run.Job{}, executionIdentity{}, kube.ErrIdentity
	}
	job, err := s.store.GetJob(r.Context(), chi.URLParam(r, "jobID"))
	_, configured := s.externalRunner(job.ExternalRunnerID())
	if err != nil || !configured || !job.Plan.External || !s.allowed(job.Plan.Repository) ||
		!time.Now().Before(job.LeaseUntil) ||
		subtle.ConstantTimeCompare([]byte(attempt), []byte(job.AttemptID)) != 1 {
		return run.Job{}, executionIdentity{}, kube.ErrIdentity
	}
	// Pending discovery (including a normal server restart) pauses new claims,
	// credentials and lease renewals. An existing attempt can still flush output
	// under its last valid lease unless revocation has been confirmed.
	if errors.Is(s.requireRepositoryAccess(job.Plan.Repository, 0), errRepositoryAuthorization) {
		return run.Job{}, executionIdentity{}, kube.ErrIdentity
	}
	return job, executionIdentity{id: attempt}, nil
}

func matchesExecution(job run.Job, id string) bool {
	if job.Plan.External {
		return job.AttemptID == id && time.Now().Before(job.LeaseUntil)
	}
	return job.PodUID == id
}

func leaseSeconds(job run.Job) int {
	return max(0, int(time.Until(job.LeaseUntil)/time.Second))
}
