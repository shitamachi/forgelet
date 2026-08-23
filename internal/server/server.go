// Package server is the forgelet control-plane composition root for M0:
// webhook ingestion, workflow compilation, dispatch, the executor-facing
// internal API and check reporting. M0 deviations (memory durable store,
// local identity verifier, local workflow source) are all injected ports
// (spec 0011 FR-D5).
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/shitamachi/forgelet/internal/provider/github"
	"github.com/shitamachi/forgelet/internal/report"
	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/run/plan"
	"github.com/shitamachi/forgelet/internal/run/schedule"
	"github.com/shitamachi/forgelet/internal/run/scheduler"
	"github.com/shitamachi/forgelet/internal/runtime/executor"
	"github.com/shitamachi/forgelet/internal/security/identity"
	"github.com/shitamachi/forgelet/internal/security/policy"
	"github.com/shitamachi/forgelet/internal/storage/memory"
	"github.com/shitamachi/forgelet/internal/workflow/compiler"
	"github.com/shitamachi/forgelet/internal/workflow/syntax"
)

// Options configures a Server. Every external dependency is a port.
type Options struct {
	WebhookSecret []byte
	WorkflowsDir  string
	// WorkflowFetcher replaces the local directory with a repository
	// source (GitHub content API adapter); nil = local dir (dev).
	WorkflowFetcher WorkflowFetcher
	// ScheduledRepos enables the internal cron scheduler (spec 0002 T9)
	// for these repositories' default branches.
	ScheduledRepos []ScheduledRepo
	SecretValues   map[string]string // "scope/name" -> plaintext (M0 demo source)
	CheckReporter  report.CheckReporter
	Active         scheduler.ActiveExecutionStore
	Durable        scheduler.DurableStore // memory adapter by default; PostgreSQL in production
	Verifier       identity.Verifier
	Issuer         identity.Issuer
	TokenKey       []byte // used when Verifier/Issuer are nil (local dev identity)
	DetailsBaseURL string
	Now            func() time.Time
	Log            *slog.Logger
}

// Server wires and exposes the M0 control plane.
type Server struct {
	opts     Options
	log      *slog.Logger
	now      func() time.Time
	durable  scheduler.DurableStore
	active   scheduler.ActiveExecutionStore
	ingest   *scheduler.Ingestor
	dispatch *scheduler.Dispatcher
	project  *scheduler.Projector
	collect  *scheduler.Collector
	sched    *schedule.Scheduler
	reporter report.CheckReporter
	plans    *planStore
	jobs     map[model.RunID][]compiler.JobInstance
	trust    map[model.JobRunID]policy.TrustLevel
	jobMu    sync.Mutex
	src      *workflowSource
	mux      chi.Router
}

// NewServer wires everything. Callers then use Handler() (HTTP) and the
// driver methods (DispatchOnce/CollectOnce) or StartLoops.
func NewServer(opts Options) (*Server, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Durable == nil {
		opts.Durable = memory.NewDurableStore(opts.Now, nil)
	}
	if opts.Active == nil {
		opts.Active = memory.NewActiveStore()
	}
	if opts.CheckReporter == nil {
		return nil, fmt.Errorf("server: CheckReporter is required")
	}
	if len(opts.TokenKey) == 0 && (opts.Verifier == nil || opts.Issuer == nil) {
		return nil, fmt.Errorf("server: TokenKey required for local identity")
	}
	verifier := opts.Verifier
	if verifier == nil {
		verifier = identity.NewLocalVerifier(opts.TokenKey, opts.Now, 30*time.Second)
	}
	issuer := opts.Issuer
	if issuer == nil {
		issuer = identity.NewLocalIssuer(opts.TokenKey, opts.Now)
	}
	opts.Verifier, opts.Issuer = verifier, issuer

	s := &Server{
		opts:     opts,
		log:      opts.Log,
		now:      opts.Now,
		durable:  opts.Durable,
		active:   opts.Active,
		reporter: opts.CheckReporter,
		plans:    newPlanStore(),
		jobs:     map[model.RunID][]compiler.JobInstance{},
		trust:    map[model.JobRunID]policy.TrustLevel{},
	}

	source := &workflowSource{dir: opts.WorkflowsDir, fetcher: opts.WorkflowFetcher, log: opts.Log}
	s.src = source
	ids := scheduler.NewIDGen(opts.Now, nil)
	s.ingest = scheduler.NewIngestor(opts.Durable, source, ids, opts.Now)
	s.dispatch = scheduler.NewDispatcher(opts.Durable, opts.Active, opts.Now)
	s.project = scheduler.NewProjector(opts.Durable, opts.Now)
	s.collect = scheduler.NewCollector(opts.Durable, opts.Active, opts.Now)
	if len(opts.ScheduledRepos) > 0 {
		s.sched = schedule.New(&repoSchedules{repos: opts.ScheduledRepos, src: source}, s, opts.Now)
	}

	s.mux = chi.NewRouter()
	s.routes()
	return s, nil
}

// Ingest records a delivery and, for a new one, compiles workflows and
// materializes plans. It satisfies the github IngestPort.
func (s *Server) Ingest(ctx context.Context, d model.Delivery) (model.RunID, bool, error) {
	runID, created, err := s.ingest.Ingest(ctx, d)
	if err != nil || runID == "" || !created {
		return runID, created, err
	}
	trust := trustFor(d)
	if err := s.buildPlans(ctx, d, runID, trust); err != nil {
		return runID, created, err
	}
	return runID, created, nil
}

// trustFor classifies a delivery: pushes are trusted; pull requests are
// same-repo or fork based on the head repository (spec 0001 FR-9.4).
func trustFor(d model.Delivery) policy.TrustLevel {
	if d.Event.Name != "pull_request" {
		return policy.TrustTrusted
	}
	var pr struct {
		PullRequest struct {
			Head struct {
				Repo struct {
					Fork bool `json:"fork"`
				} `json:"repo"`
			} `json:"head"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(d.Payload, &pr); err != nil {
		return policy.TrustSameRepo // unparsable: least privilege that still runs
	}
	if pr.PullRequest.Head.Repo.Fork {
		return policy.TrustFork
	}
	return policy.TrustSameRepo
}

// buildPlans compiles the matching workflows again (deterministic) and
// stores an executor plan per job run.
func (s *Server) buildPlans(ctx context.Context, d model.Delivery, runID model.RunID, trust policy.TrustLevel) error {
	jobs, err := s.src.matchedJobs(ctx, d.Event, d.Payload)
	if err != nil {
		return fmt.Errorf("server: rebuild jobs for %s: %w", runID, err)
	}
	s.jobMu.Lock()
	s.jobs[runID] = jobs
	s.jobMu.Unlock()

	runs, err := s.durable.ListJobRuns(ctx, runID)
	if err != nil {
		return fmt.Errorf("server: list jobs of %s: %w", runID, err)
	}
	for _, rec := range runs {
		var inst *compiler.JobInstance
		for i := range jobs {
			if jobs[i].Key == rec.JobKey {
				inst = &jobs[i]
				break
			}
		}
		if inst == nil {
			return fmt.Errorf("server: job %s missing from compiled set", rec.JobKey)
		}
		p := buildPlan(rec, d.Event, *inst)
		if err := s.plans.Put(p); err != nil {
			return fmt.Errorf("server: store plan for %s: %w", rec.ID, err)
		}
		s.jobMu.Lock()
		s.trust[rec.ID] = trust
		s.jobMu.Unlock()
		s.reportCheck(ctx, rec.ID) // queued while waiting for dispatch
	}
	return nil
}

// DispatchOnce drains the queue once and reports queued checks.
func (s *Server) DispatchOnce(ctx context.Context) (int, error) {
	n := 0
	for {
		job, err := s.dispatch.DispatchNext(ctx)
		if err != nil {
			if err == scheduler.ErrNoQueuedJob { //nolint:errorlint // sentinel identity check is sufficient
				return n, nil
			}
			return n, err
		}
		n++
		s.reportCheck(ctx, job.ID)
	}
}

// CollectOnce GCs terminal active objects.
func (s *Server) CollectOnce(ctx context.Context) (int, error) { return s.collect.Collect(ctx) }

// ScheduleOnce ticks the internal cron scheduler once (spec 0002 T9) and
// returns how many schedule deliveries were emitted. It is a no-op unless
// Options.ScheduledRepos is set.
func (s *Server) ScheduleOnce(ctx context.Context) (int, error) {
	if s.sched == nil {
		return 0, nil
	}
	return s.sched.Tick(ctx)
}

// MintJobToken issues the executor identity token for a job run (M0 local
// identity; in-cluster this becomes a projected ServiceAccount token).
func (s *Server) MintJobToken(ctx context.Context, id model.JobRunID) (string, error) {
	return s.opts.Issuer.Issue(ctx, identity.Identity{
		Audience:  identity.Audience,
		Namespace: "forgelet-jobs",
		PodUID:    "pod-" + string(id),
		JobRunID:  id,
		Scopes:    []string{identity.ScopePlanRead, identity.ScopeSecretsRead, identity.ScopeStatusWrite},
		ExpiresAt: s.now().Add(identity.MaxTTL),
		IssuedAt:  s.now(),
	})
}

// Handler returns the HTTP handler (webhook + internal API + health).
func (s *Server) Handler() http.Handler { return s.mux }

// StartLoops runs dispatch and collection until ctx is done.
func (s *Server) StartLoops(ctx context.Context) {
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if _, err := s.DispatchOnce(ctx); err != nil {
					s.log.Error("dispatch loop", "err", err.Error())
				}
			}
		}
	}()
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if _, err := s.CollectOnce(ctx); err != nil {
					s.log.Error("collect loop", "err", err.Error())
				}
			}
		}
	}()
	if s.sched != nil {
		go func() {
			t := time.NewTicker(30 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if _, err := s.ScheduleOnce(ctx); err != nil {
						s.log.Error("schedule loop", "err", err.Error())
					}
				}
			}
		}()
	}
}

func (s *Server) routes() {
	s.mux.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	s.mux.Post("/webhooks/github", github.NewWebhookHandler(s.opts.WebhookSecret, s).ServeHTTP)

	s.mux.Group(func(r chi.Router) {
		r.Use(s.auth)
		r.Get("/internal/jobruns/{id}/plan", s.handlePlan)
		r.Post("/internal/jobruns/{id}/secrets", s.handleSecrets)
		r.Post("/internal/jobruns/{id}/status", s.handleStatus)
		r.Post("/internal/jobruns/{id}/observed", s.handleObserved)
	})
}

// auth verifies the executor/controller identity token.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}
		id, err := s.opts.Verifier.Verify(r.Context(), strings.TrimPrefix(header, "Bearer "))
		if err != nil {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), id)))
	})
}

func (s *Server) jobFromPath(w http.ResponseWriter, r *http.Request) (model.JobRunID, identity.Identity, bool) {
	id := model.JobRunID(chi.URLParam(r, "id"))
	if id == "" {
		http.Error(w, "missing job run id", http.StatusBadRequest)
		return "", identity.Identity{}, false
	}
	ident, ok := identityFrom(r.Context())
	if !ok {
		http.Error(w, "missing identity", http.StatusUnauthorized)
		return "", identity.Identity{}, false
	}
	if err := policy.AuthorizeExecution(ident, id); err != nil {
		http.Error(w, "identity not bound to this job run", http.StatusForbidden)
		return "", identity.Identity{}, false
	}
	return id, ident, true
}

func (s *Server) handlePlan(w http.ResponseWriter, r *http.Request) {
	id, ident, ok := s.jobFromPath(w, r)
	if !ok {
		return
	}
	if !ident.HasScope(identity.ScopePlanRead) {
		http.Error(w, "missing scope plan:read", http.StatusForbidden)
		return
	}
	p, err := s.plans.Get(id)
	if err != nil {
		http.Error(w, "plan not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleSecrets(w http.ResponseWriter, r *http.Request) {
	id, ident, ok := s.jobFromPath(w, r)
	if !ok {
		return
	}
	if !ident.HasScope(identity.ScopeSecretsRead) {
		http.Error(w, "missing scope secrets:read", http.StatusForbidden)
		return
	}
	var req []struct {
		Scope string `json:"scope"`
		Name  string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}
	p, err := s.plans.Get(id)
	if err != nil {
		http.Error(w, "plan not found", http.StatusNotFound)
		return
	}
	requested := make([]policy.Ref, 0, len(req))
	for _, rr := range req {
		requested = append(requested, policy.Ref{Scope: rr.Scope, Name: rr.Name})
	}
	planRefs := make([]policy.Ref, 0, len(p.SecretRefs))
	for _, sr := range p.SecretRefs {
		planRefs = append(planRefs, policy.Ref{Scope: sr.Scope, Name: sr.Name})
	}
	s.jobMu.Lock()
	trust := s.trust[id]
	s.jobMu.Unlock()
	decision := policy.DecideSecrets(ident, requested, planRefs, trust)
	out := map[string]string{}
	for _, allowed := range decision.Allowed {
		if v, okv := s.opts.SecretValues[allowed.Scope+"/"+allowed.Name]; okv {
			out[allowed.Scope+"/"+allowed.Name] = v
		}
	}
	for _, denied := range decision.Denied {
		s.log.Warn("secret request denied", "jobRun", string(id), "scope", denied.Ref.Scope, "name", denied.Ref.Name, "reason", denied.Reason)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	id, ident, ok := s.jobFromPath(w, r)
	if !ok {
		return
	}
	if !ident.HasScope(identity.ScopeStatusWrite) {
		http.Error(w, "missing scope status:write", http.StatusForbidden)
		return
	}
	var result executor.JobResult
	if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
		http.Error(w, "malformed result", http.StatusBadRequest)
		return
	}
	phase := model.PhaseFailed
	if result.Success {
		phase = model.PhaseSucceeded
	}
	if err := s.project.Project(r.Context(), id, phase); err != nil {
		http.Error(w, "projection failed", http.StatusInternalServerError)
		return
	}
	s.reportCheck(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleObserved(w http.ResponseWriter, r *http.Request) {
	id, _, ok := s.jobFromPath(w, r)
	if !ok {
		return
	}
	var body struct {
		Phase model.ObservedPhase `json:"phase"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "malformed body", http.StatusBadRequest)
		return
	}
	if err := s.project.Project(r.Context(), id, body.Phase); err != nil {
		http.Error(w, "projection failed", http.StatusInternalServerError)
		return
	}
	s.reportCheck(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// reportCheck pushes the current durable job state to the provider.
func (s *Server) reportCheck(ctx context.Context, id model.JobRunID) {
	job, err := s.durable.GetJobRun(ctx, id)
	if err != nil {
		s.log.Error("report check: load job", "jobRun", string(id), "err", err.Error())
		return
	}
	run, err := s.durable.GetRun(ctx, job.RunID)
	if err != nil {
		s.log.Error("report check: load run", "jobRun", string(id), "err", err.Error())
		return
	}
	check, err := report.MapJobRun(job, s.opts.DetailsBaseURL)
	if err != nil {
		s.log.Error("report check: map", "jobRun", string(id), "err", err.Error())
		return
	}
	if err := s.reporter.Report(ctx, run, check); err != nil {
		s.log.Error("report check", "jobRun", string(id), "err", err.Error())
	}
}

type ctxKey int

const identityKey ctxKey = iota

func withIdentity(ctx context.Context, id identity.Identity) context.Context {
	return context.WithValue(ctx, identityKey, id)
}

func identityFrom(ctx context.Context) (identity.Identity, bool) {
	id, ok := ctx.Value(identityKey).(identity.Identity)
	return id, ok
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WorkflowFetcher is the repository workflow source port (the GitHub
// content adapter implements it; the local directory is the dev fallback).
type WorkflowFetcher interface {
	FetchWorkflows(ctx context.Context, repo model.RepositoryRef, ref string) ([]WorkflowFile, error)
}

// WorkflowFile is one workflow file from a source.
type WorkflowFile struct {
	Name string
	Data []byte
}

// workflowSource compiles workflows from a repository fetcher or a local
// directory.
type workflowSource struct {
	dir     string
	fetcher WorkflowFetcher
	log     *slog.Logger
}

// Compile implements scheduler.Compiler.
func (w *workflowSource) Compile(ctx context.Context, ev model.Event, payload []byte) ([]model.JobIntent, error) {
	jobs, err := w.matchedJobs(ctx, ev, payload)
	if err != nil {
		return nil, err
	}
	intents := make([]model.JobIntent, 0, len(jobs))
	for _, j := range jobs {
		intent := model.JobIntent{JobKey: j.Key, RunnerClass: j.RunnerClass, DependsOn: j.DependsOn, Matrix: j.Matrix}
		intents = append(intents, intent)
	}
	return intents, nil
}

func (w *workflowSource) matchedJobs(ctx context.Context, ev model.Event, payload []byte) ([]compiler.JobInstance, error) {
	if ev.Name != "push" && ev.Name != "pull_request" && ev.Name != "schedule" {
		return nil, nil
	}
	ref := ev.SHA
	if ev.Name == "schedule" {
		ref = ev.Ref
	}
	files, err := w.load(ctx, ev.Repository, ref)
	if err != nil {
		return nil, fmt.Errorf("workflow source: %w", err)
	}
	var out []compiler.JobInstance
	for _, f := range files {
		wf, err := syntax.Parse(f.Name, f.Data)
		if err != nil {
			return nil, fmt.Errorf("workflow source: %w", err)
		}
		c, err := compiler.Compile(wf)
		if err != nil {
			return nil, fmt.Errorf("workflow source: %w", err)
		}
		switch ev.Name {
		case "push":
			if c.MatchesPush(ev.Ref) {
				out = append(out, c.Jobs...)
			}
		case "pull_request":
			var pr struct {
				PullRequest struct {
					Base struct {
						Ref string `json:"ref"`
					} `json:"base"`
				} `json:"pull_request"`
			}
			_ = json.Unmarshal(payload, &pr)
			if c.MatchesPullRequest(pr.PullRequest.Base.Ref) {
				out = append(out, c.Jobs...)
			}
		case "schedule":
			if len(c.Schedules()) > 0 {
				out = append(out, c.Jobs...)
			}
		}
	}
	return out, nil
}

func (w *workflowSource) load(ctx context.Context, repo model.RepositoryRef, ref string) ([]WorkflowFile, error) {
	if w.fetcher != nil {
		files, err := w.fetcher.FetchWorkflows(ctx, repo, ref)
		if err != nil {
			return nil, err
		}
		return files, nil
	}
	local, err := listYAML(w.dir)
	if err != nil {
		return nil, err
	}
	out := make([]WorkflowFile, 0, len(local))
	for _, f := range local {
		out = append(out, WorkflowFile{Name: f.name, Data: f.data})
	}
	return out, nil
}

type yamlFile struct {
	name string
	data []byte
}

func listYAML(dir string) ([]yamlFile, error) {
	if dir == "" {
		return nil, fmt.Errorf("workflows dir not configured")
	}
	entries, err := readDirSorted(dir)
	if err != nil {
		return nil, err
	}
	var out []yamlFile
	for _, e := range entries {
		if !strings.HasSuffix(e.name, ".yml") && !strings.HasSuffix(e.name, ".yaml") {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// buildPlan converts a compiled job instance into an executor plan.
// Environment values that are exactly `${{ secrets.NAME }}` become secret
// references resolved at execution time (minimal M0 interpolation; the full
// template engine is 0007 T7).
func buildPlan(rec model.JobRunRecord, ev model.Event, inst compiler.JobInstance) *plan.Plan {
	p := &plan.Plan{
		JobRunID:    rec.ID,
		Repository:  ev.Repository,
		SHA:         ev.SHA,
		Ref:         ev.Ref,
		RunnerClass: inst.RunnerClass,
		Env:         map[string]string{},
	}
	for k, v := range inst.Env {
		if name, ok := secretRefName(v); ok {
			p.SecretRefs = append(p.SecretRefs, plan.SecretRef{Scope: "repository", Name: name, Env: k})
			continue
		}
		p.Env[k] = v
	}
	for _, st := range inst.Steps {
		p.Steps = append(p.Steps, plan.Step{ID: stDisplayName(st), Name: st.Name, Run: plan.RunStep{Script: st.Run, Env: st.Env}})
	}
	return p
}

func stDisplayName(st compiler.Step) string {
	if st.Name != "" {
		return st.Name
	}
	// Steps without names are identified by their script's first token.
	return truncate(strings.Fields(st.Run)[0], 24)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// secretRefName recognizes `${{ secrets.NAME }}` verbatim.
func secretRefName(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "${{ secrets.") || !strings.HasSuffix(v, "}}") {
		return "", false
	}
	name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(v, "${{ secrets."), "}}"))
	if name == "" {
		return "", false
	}
	return name, true
}
