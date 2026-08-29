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

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/shitamachi/forgelet/internal/observability/metrics"
	"github.com/shitamachi/forgelet/internal/observability/tracing"
	"github.com/shitamachi/forgelet/internal/provider/github"
	"github.com/shitamachi/forgelet/internal/report"
	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/run/plan"
	"github.com/shitamachi/forgelet/internal/run/schedule"
	"github.com/shitamachi/forgelet/internal/run/scheduler"
	"github.com/shitamachi/forgelet/internal/runtime/executor"
	"github.com/shitamachi/forgelet/internal/security/identity"
	"github.com/shitamachi/forgelet/internal/security/policy"
	"github.com/shitamachi/forgelet/internal/security/secret"
	"github.com/shitamachi/forgelet/internal/storage/memory"
	"github.com/shitamachi/forgelet/internal/storage/s3"
	"github.com/shitamachi/forgelet/internal/workflow/compiler"
	"github.com/shitamachi/forgelet/internal/workflow/expression"
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
	SecretStore    secret.Store      // PG-backed sealed secrets; nil falls back to SecretValues (dev)
	CheckReporter  report.CheckReporter
	Active         scheduler.ActiveExecutionStore
	Durable        scheduler.DurableStore // memory adapter by default; PostgreSQL in production
	Metrics        *metrics.Metrics       // Prometheus instrumentation; default instance when nil
	TracerProvider trace.TracerProvider   // OpenTelemetry; no-op when nil
	S3             *s3.Store              // S3/MinIO presigned URLs for cache/artifacts; nil disables
	Verifier       identity.Verifier
	Issuer         identity.Issuer
	TokenKey       []byte // used when Verifier/Issuer are nil (local dev identity)
	DetailsBaseURL string
	Now            func() time.Time
	Log            *slog.Logger

	tracer trace.Tracer
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
	handler  http.Handler
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
	if opts.Metrics == nil {
		opts.Metrics = metrics.New()
	}
	if opts.TracerProvider == nil {
		opts.TracerProvider = noop.NewTracerProvider()
	}
	opts.tracer = opts.TracerProvider.Tracer("github.com/shitamachi/forgelet/internal/server")
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
	if af, ok := opts.WorkflowFetcher.(github.ActionFetcher); ok {
		source.actionFetcher = af
	}
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
	s.mux.Handle("/metrics", s.opts.Metrics.Handler())
	s.routes()
	s.handler = tracing.Middleware(opts.TracerProvider, "forgelet-server", s.mux)
	return s, nil
}

// Ingest records a delivery and, for a new one, compiles workflows and
// materializes plans. It satisfies the github IngestPort.
func (s *Server) Ingest(ctx context.Context, d model.Delivery) (model.RunID, bool, error) {
	ctx, span := s.opts.tracer.Start(ctx, "server.ingest",
		trace.WithAttributes(attribute.String("forgelet.event.name", d.Event.Name)))
	defer span.End()
	runID, created, err := s.ingest.Ingest(ctx, d)
	if err != nil || runID == "" || !created {
		return runID, created, err
	}
	trust := trustFor(d)
	if err := s.buildPlans(ctx, d, runID, trust); err != nil {
		span.RecordError(err)
		return runID, created, err
	}
	return runID, created, nil
}

// Rerequest creates a new attempt for a JobRun (spec 0005 T8, FR-8.4).
// It copies the plan and trust level so the new attempt is immediately
// dispatchable.
func (s *Server) Rerequest(ctx context.Context, id model.JobRunID) (model.JobRunID, error) {
	newID, err := s.durable.RerequestJob(ctx, id, s.now())
	if err != nil {
		return "", err
	}
	if oldPlan, err := s.plans.Get(id); err == nil {
		newPlan := *oldPlan
		newPlan.JobRunID = newID
		_ = s.plans.Put(&newPlan)
	}
	s.jobMu.Lock()
	if trust, ok := s.trust[id]; ok {
		s.trust[newID] = trust
	}
	s.jobMu.Unlock()
	s.reportCheck(ctx, newID)
	return newID, nil
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
		// Job-level condition (spec 0006 V1): github-only conditions are
		// evaluated eagerly here; needs/status conditions are deferred to
		// dispatch time when dependency results are final.
		if inst.If != "" && !isDeferredCondition(inst.If) {
			run, err := expression.EvaluateCondition(inst.If, jobConditionEnv(d.Event, rec, runs))
			switch {
			case err != nil:
				return fmt.Errorf("server: job %s: invalid if %q: %w", rec.JobKey, inst.If, err)
			case !run:
				if perr := s.projectObserved(ctx, rec.ID, model.PhaseSkipped); perr != nil {
					return fmt.Errorf("server: skip job %s: %w", rec.ID, perr)
				}
				s.reportCheck(ctx, rec.ID)
				continue
			}
		}
		p, err := s.buildPlan(ctx, rec, d.Event, *inst)
		if err != nil {
			return fmt.Errorf("server: build plan for %s: %w", rec.ID, err)
		}
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

// isDeferredCondition reports whether cond must be evaluated at dispatch time
// because it references needs results or status functions that are not final
// until dependencies finish. Such conditions are not rejected at ingest.
func isDeferredCondition(cond string) bool {
	lower := strings.ToLower(cond)
	return strings.Contains(lower, "needs.") ||
		strings.Contains(lower, "success(") || strings.Contains(lower, "failure(") ||
		strings.Contains(lower, "cancelled(") || strings.Contains(lower, "always(")
}

// jobConditionEnv builds the scheduler-time evaluation environment for
// `if:` conditions: github plus needs.<job>.result. env/secrets/steps are
// unavailable here by design (GitHub evaluates job conditions pre-run).
func jobConditionEnv(ev model.Event, rec model.JobRunRecord, runs []model.JobRunRecord) *expression.Env {
	needs := map[string]expression.Value{}
	for _, r := range runs {
		needs[r.JobKey] = expression.ObjOf(map[string]expression.Value{
			"result": expression.StrOf(githubResultName(r.Status)),
		})
	}
	jobStatus := "success"
	for _, depKey := range rec.DependsOn {
		for _, r := range runs {
			if r.JobKey == depKey && (r.Status == model.JobFailed || r.Status == model.JobCancelled) {
				jobStatus = "failure"
				if r.Status == model.JobCancelled {
					jobStatus = "cancelled"
				}
				break
			}
		}
	}
	env := expression.NewEnv().
		With("github", githubContextValue(ev)).
		With("needs", expression.ObjOf(needs)).
		With("job", expression.ObjOf(map[string]expression.Value{
			"status": expression.StrOf(jobStatus),
		}))
	return env.WithJobStatus(jobStatus)
}

// githubContextValue exposes the event as the `github` expression context.
func githubContextValue(ev model.Event) expression.Value {
	return expression.ObjOf(map[string]expression.Value{
		"event_name": expression.StrOf(ev.Name),
		"ref":        expression.StrOf(ev.Ref),
		"sha":        expression.StrOf(ev.SHA),
		"actor":      expression.StrOf(ev.Actor),
		"repository": expression.ObjOf(map[string]expression.Value{
			"owner":     expression.StrOf(ev.Repository.Owner),
			"name":      expression.StrOf(ev.Repository.Name),
			"full_name": expression.StrOf(ev.Repository.Owner + "/" + ev.Repository.Name),
		}),
	})
}

// evaluateDeferredConditions scans queued jobs whose `if:` was deferred
// (needs/status) and marks those whose condition is now false as skipped.
// It loops until stable to handle transitive skips.
func (s *Server) evaluateDeferredConditions(ctx context.Context) {
	for {
		queued, err := s.durable.ListQueuedJobs(ctx)
		if err != nil || len(queued) == 0 {
			return
		}
		// Group queued jobs by run for needs context.
		byRun := map[model.RunID][]model.JobRunRecord{}
		for _, j := range queued {
			byRun[j.RunID] = append(byRun[j.RunID], j)
		}
		progress := false
		for runID, qs := range byRun {
			// Need the run's event for github context and all jobs for needs.
			run, err := s.durable.GetRun(ctx, runID)
			if err != nil {
				continue
			}
			all, err := s.durable.ListJobRuns(ctx, runID)
			if err != nil {
				continue
			}
			for _, rec := range qs {
				if rec.Condition == "" || !isDeferredCondition(rec.Condition) {
					continue
				}
				// Only evaluate when dependencies are terminal (or no deps).
				// If any dependency is still pending/running, defer again.
				ready := true
				for _, depKey := range rec.DependsOn {
					for _, sibling := range all {
						if sibling.JobKey == depKey && !sibling.Status.IsTerminal() {
							ready = false
							break
						}
					}
				}
				if !ready {
					continue
				}
				runCond, err := expression.EvaluateCondition(rec.Condition, jobConditionEnv(run.Event, rec, all))
				if err != nil {
					// Invalid condition at dispatch time: fail the job so it
					// does not hang forever. The run will be marked failed.
					_ = s.projectObserved(ctx, rec.ID, model.PhaseFailed)
					s.reportCheck(ctx, rec.ID)
					progress = true
					continue
				}
				if !runCond {
					_ = s.projectObserved(ctx, rec.ID, model.PhaseSkipped)
					s.reportCheck(ctx, rec.ID)
					progress = true
				}
			}
		}
		if !progress {
			return
		}
	}
}

func githubResultName(s model.JobRunStatus) string {
	switch s {
	case model.JobSucceeded:
		return "success"
	case model.JobFailed:
		return "failure"
	case model.JobCancelled:
		return "cancelled"
	case model.JobSkipped:
		return "skipped"
	default:
		return "pending"
	}
}

// DispatchOnce drains the queue once and reports queued checks. It feeds
// the dispatch-latency histogram and the queue-depth gauge (0010 FR-O3).
func (s *Server) DispatchOnce(ctx context.Context) (int, error) {
	ctx, span := s.opts.tracer.Start(ctx, "server.dispatch_drain")
	defer span.End()
	s.evaluateDeferredConditions(ctx)
	n := 0
	for {
		job, err := s.dispatch.DispatchNext(ctx)
		if err != nil {
			if err == scheduler.ErrNoQueuedJob { //nolint:errorlint // sentinel identity check is sufficient
				break
			}
			span.RecordError(err)
			s.publishQueueDepth(ctx)
			return n, err
		}
		n++
		_, dspan := s.opts.tracer.Start(ctx, "server.dispatch",
			trace.WithAttributes(attribute.String("forgelet.jobrun.id", string(job.ID))))
		if rec, gerr := s.durable.GetJobRun(ctx, job.ID); gerr == nil && rec.DispatchedAt != nil {
			s.opts.Metrics.ObserveDispatch(rec.CreatedAt, *rec.DispatchedAt)
		}
		s.reportCheck(ctx, job.ID)
		dspan.End()
	}
	s.publishQueueDepth(ctx)
	return n, nil
}

func (s *Server) publishQueueDepth(ctx context.Context) {
	if n, err := s.durable.CountQueuedJobs(ctx); err == nil {
		s.opts.Metrics.SetQueueDepth(n)
	}
}

// projectObserved projects a phase through the monotonic channel and, on
// terminal outcomes, records duration/completion metrics.
func (s *Server) projectObserved(ctx context.Context, id model.JobRunID, phase model.ObservedPhase) error {
	ctx, span := s.opts.tracer.Start(ctx, "server.project",
		trace.WithAttributes(
			attribute.String("forgelet.jobrun.id", string(id)),
			attribute.String("forgelet.phase", string(phase)),
		))
	defer span.End()
	if err := s.project.Project(ctx, id, phase); err != nil {
		span.RecordError(err)
		return err
	}
	if rec, err := s.durable.GetJobRun(ctx, id); err == nil {
		s.opts.Metrics.ObserveCompletion(rec)
	}
	return nil
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

// Handler returns the HTTP handler (webhook + internal API + health +
// metrics), wrapped in the tracing middleware.
func (s *Server) Handler() http.Handler { return s.handler }

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
	s.mux.Post("/webhooks/github",
		github.NewWebhookHandlerWithLogger(s.opts.WebhookSecret, s, s.log).WithRerequest(s).ServeHTTP)

	s.mux.Group(func(r chi.Router) {
		r.Use(s.auth)
		r.Get("/internal/jobruns/{id}/plan", s.handlePlan)
		r.Post("/internal/jobruns/{id}/secrets", s.handleSecrets)
		r.Post("/internal/jobruns/{id}/status", s.handleStatus)
		r.Post("/internal/jobruns/{id}/observed", s.handleObserved)
		r.Post("/internal/jobruns/{id}/cache/resolve", s.handleCacheResolve)
		r.Post("/internal/jobruns/{id}/artifacts/{name}", s.handleArtifactUpload)
		r.Get("/internal/jobruns/{id}/artifacts/{name}", s.handleArtifactDownload)
	})
	// Management API for sealed secrets (spec 0003 T7). Plaintext values are
	// sealed server-side and never logged. No separate auth in dev; production
	// should front this with an admin token or mTLS (tracked separately).
	s.mux.Get("/api/secrets", s.handleSecretList)
	s.mux.Post("/api/secrets", s.handleSecretPut)
	s.mux.Delete("/api/secrets/{scope}/{name}", s.handleSecretDelete)
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
		if s.opts.SecretStore != nil {
			if v, err := s.opts.SecretStore.GetSecret(r.Context(), allowed.Scope, allowed.Name); err == nil {
				out[allowed.Scope+"/"+allowed.Name] = v
				continue
			} else {
				s.log.Warn("secret store miss", "scope", allowed.Scope, "name", allowed.Name, "err", err.Error())
			}
		}
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
	if err := s.projectObserved(r.Context(), id, phase); err != nil {
		http.Error(w, "projection failed", http.StatusInternalServerError)
		return
	}
	s.reportCheck(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleObserved(w http.ResponseWriter, r *http.Request) {
	id := model.JobRunID(chi.URLParam(r, "id"))
	if id == "" {
		http.Error(w, "missing job run id", http.StatusBadRequest)
		return
	}
	ident, ok := identityFrom(r.Context())
	if !ok {
		http.Error(w, "missing identity", http.StatusUnauthorized)
		return
	}
	// Observed-phase projection is the controller's job: it holds
	// observed:write and is not bound to one JobRun (executors projecting
	// their own terminal result use /status instead).
	if err := policy.AuthorizeObservation(ident, id); err != nil {
		http.Error(w, "identity may not observe this job run", http.StatusForbidden)
		return
	}
	var body struct {
		Phase model.ObservedPhase `json:"phase"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "malformed body", http.StatusBadRequest)
		return
	}
	if err := s.projectObserved(r.Context(), id, body.Phase); err != nil {
		http.Error(w, "projection failed", http.StatusInternalServerError)
		return
	}
	s.reportCheck(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleCacheResolve(w http.ResponseWriter, r *http.Request) {
	id, ident, ok := s.jobFromPath(w, r)
	if !ok {
		return
	}
	if !ident.HasScope(identity.ScopePlanRead) && !ident.HasScope(identity.ScopeStatusWrite) {
		http.Error(w, "missing scope", http.StatusForbidden)
		return
	}
	if s.opts.S3 == nil {
		http.Error(w, "cache storage not configured", http.StatusNotImplemented)
		return
	}
	var body struct {
		Key         string   `json:"key"`
		RestoreKeys []string `json:"restoreKeys"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "malformed body", http.StatusBadRequest)
		return
	}
	if body.Key == "" {
		http.Error(w, "key is required", http.StatusBadRequest)
		return
	}
	job, err := s.durable.GetJobRun(r.Context(), id)
	if err != nil {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}
	run, err := s.durable.GetRun(r.Context(), job.RunID)
	if err != nil {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	repo := run.Event.Repository
	hit, hitKey, err := s.opts.S3.CacheResolve(r.Context(), repo, body.Key, body.RestoreKeys)
	if err != nil {
		http.Error(w, "cache resolve failed", http.StatusInternalServerError)
		return
	}
	putURL, err := s.opts.S3.CachePutURL(r.Context(), repo, body.Key)
	if err != nil {
		http.Error(w, "presign put failed", http.StatusInternalServerError)
		return
	}
	resp := map[string]any{"hit": hit, "putUrl": putURL}
	if hit {
		getURL, err := s.opts.S3.CacheGetURL(r.Context(), hitKey)
		if err != nil {
			http.Error(w, "presign get failed", http.StatusInternalServerError)
			return
		}
		resp["getUrl"] = getURL
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleArtifactUpload(w http.ResponseWriter, r *http.Request) {
	id, ident, ok := s.jobFromPath(w, r)
	if !ok {
		return
	}
	if !ident.HasScope(identity.ScopeStatusWrite) {
		http.Error(w, "missing scope status:write", http.StatusForbidden)
		return
	}
	if s.opts.S3 == nil {
		http.Error(w, "artifact storage not configured", http.StatusNotImplemented)
		return
	}
	name := chi.URLParam(r, "name")
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "..") {
		http.Error(w, "invalid artifact name", http.StatusBadRequest)
		return
	}
	job, err := s.durable.GetJobRun(r.Context(), id)
	if err != nil {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}
	run, err := s.durable.GetRun(r.Context(), job.RunID)
	if err != nil {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	u, err := s.opts.S3.ArtifactPutURL(r.Context(), run.Event.Repository, job.RunID, name)
	if err != nil {
		http.Error(w, "presign failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"uploadUrl": u})
}

func (s *Server) handleArtifactDownload(w http.ResponseWriter, r *http.Request) {
	id, ident, ok := s.jobFromPath(w, r)
	if !ok {
		return
	}
	if !ident.HasScope(identity.ScopePlanRead) {
		http.Error(w, "missing scope plan:read", http.StatusForbidden)
		return
	}
	if s.opts.S3 == nil {
		http.Error(w, "artifact storage not configured", http.StatusNotImplemented)
		return
	}
	name := chi.URLParam(r, "name")
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "..") {
		http.Error(w, "invalid artifact name", http.StatusBadRequest)
		return
	}
	job, err := s.durable.GetJobRun(r.Context(), id)
	if err != nil {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}
	run, err := s.durable.GetRun(r.Context(), job.RunID)
	if err != nil {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	u, err := s.opts.S3.ArtifactGetURL(r.Context(), run.Event.Repository, job.RunID, name)
	if err != nil {
		http.Error(w, "presign failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"downloadUrl": u})
}

func (s *Server) handleSecretPut(w http.ResponseWriter, r *http.Request) {
	if s.opts.SecretStore == nil {
		http.Error(w, "secret storage not configured", http.StatusNotImplemented)
		return
	}
	var body struct {
		Scope string `json:"scope"`
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "malformed body", http.StatusBadRequest)
		return
	}
	if body.Scope == "" || body.Name == "" || body.Value == "" {
		http.Error(w, "scope, name and value are required", http.StatusBadRequest)
		return
	}
	if err := s.opts.SecretStore.PutSecret(r.Context(), body.Scope, body.Name, body.Value); err != nil {
		http.Error(w, "put secret failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSecretList(w http.ResponseWriter, r *http.Request) {
	if s.opts.SecretStore == nil {
		http.Error(w, "secret storage not configured", http.StatusNotImplemented)
		return
	}
	list, err := s.opts.SecretStore.ListSecrets(r.Context())
	if err != nil {
		http.Error(w, "list secrets failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleSecretDelete(w http.ResponseWriter, r *http.Request) {
	if s.opts.SecretStore == nil {
		http.Error(w, "secret storage not configured", http.StatusNotImplemented)
		return
	}
	scope := chi.URLParam(r, "scope")
	name := chi.URLParam(r, "name")
	if scope == "" || name == "" {
		http.Error(w, "scope and name are required", http.StatusBadRequest)
		return
	}
	if err := s.opts.SecretStore.DeleteSecret(r.Context(), scope, name); err != nil {
		http.Error(w, "delete secret failed", http.StatusInternalServerError)
		return
	}
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
	dir           string
	fetcher       WorkflowFetcher
	actionFetcher github.ActionFetcher
	log           *slog.Logger
}

// Compile implements scheduler.Compiler.
func (w *workflowSource) Compile(ctx context.Context, ev model.Event, payload []byte) ([]model.JobIntent, error) {
	jobs, err := w.matchedJobs(ctx, ev, payload)
	if err != nil {
		return nil, err
	}
	intents := make([]model.JobIntent, 0, len(jobs))
	for _, j := range jobs {
		intent := model.JobIntent{JobKey: j.Key, RunnerClass: j.RunnerClass, DependsOn: j.DependsOn, Matrix: j.Matrix, Condition: j.If}
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
func (s *Server) buildPlan(ctx context.Context, rec model.JobRunRecord, ev model.Event, inst compiler.JobInstance) (*plan.Plan, error) {
	p := &plan.Plan{
		JobRunID:    rec.ID,
		Repository:  ev.Repository,
		EventName:   ev.Name,
		Actor:       ev.Actor,
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
		id := stDisplayName(st)
		ps := plan.Step{
			ID: id, Name: st.Name, If: st.If,
			ContinueOnError: st.ContinueOnError,
		}
		switch {
		case st.Uses != nil:
			inputs := make(map[string]string, len(st.Uses.Inputs))
			for k, v := range st.Uses.Inputs {
				if name, ok := secretRefName(v); ok {
					p.SecretRefs = append(p.SecretRefs,
						plan.SecretRef{Scope: "repository", Name: name, Env: "$with:" + id + ":" + k})
					continue
				}
				inputs[k] = v
			}
			ps.Builtin = &plan.BuiltinStep{Action: st.Uses.Action, Version: st.Uses.Version, Inputs: inputs}
		case st.RawUses != "":
			owner, repo, subpath, ref, err := github.ParseUses(st.RawUses)
			if err != nil {
				owner, repo, subpath, ref = "", st.RawUses, "", "main"
			}
			inputs := make(map[string]string, len(st.RawWith))
			for k, v := range st.RawWith {
				if name, ok := secretRefName(v); ok {
					p.SecretRefs = append(p.SecretRefs,
						plan.SecretRef{Scope: "repository", Name: name, Env: "$with:" + id + ":" + k})
					continue
				}
				inputs[k] = v
			}
			// Try to fetch action.yml to decide JS vs composite when a
			// fetcher is available (GitHub mode). Otherwise fall back to JS.
			if s.src.actionFetcher != nil {
				if meta, err := s.src.actionFetcher.FetchAction(ctx, owner, repo, ref, subpath); err == nil {
					if strings.HasPrefix(meta.RunsUsing, "composite") {
						// Expand composite steps inline.
						steps, err := s.expandComposite(meta.Steps, inputs, id)
						if err == nil {
							// Composite expansion replaces the single step with its inner steps.
							p.Steps = append(p.Steps, steps...)
							continue
						}
					} else if strings.HasPrefix(meta.RunsUsing, "node") {
						ps.JS = &plan.JSStep{Repo: owner + "/" + repo, Ref: ref, Path: subpath, Main: meta.Main, Inputs: inputs}
						// For actions/github-script the script is in with.script
						if s, ok := inputs["script"]; ok {
							ps.JS.Script = s
						}
						break
					}
				}
			}
			ps.JS = &plan.JSStep{Repo: owner + "/" + repo, Ref: ref, Path: subpath, Inputs: inputs}
			if s, ok := inputs["script"]; ok {
				ps.JS.Script = s
			}
		default:
			ps.Run = plan.RunStep{Script: st.Run, Env: st.Env}
		}
		p.Steps = append(p.Steps, ps)
	}
	return p, nil
}

// expandComposite turns a composite action's runs.steps into plan steps.
func (s *Server) expandComposite(steps []github.CompositeActionStep, outerInputs map[string]string, outerID string) ([]plan.Step, error) {
	var out []plan.Step
	for i, cs := range steps {
		id := fmt.Sprintf("%s-%d", outerID, i)
		// Interpolate inputs.* in the composite step's run/uses/with
		// For V1 we handle simple ${{ inputs.* }} substitution.
		ps := plan.Step{ID: id, Name: cs.Name}
		if cs.Run != "" {
			script := cs.Run
			for k, v := range outerInputs {
				script = strings.ReplaceAll(script, "${{ inputs."+k+" }}", v)
				script = strings.ReplaceAll(script, "${{inputs."+k+"}}", v)
			}
			ps.Run = plan.RunStep{Script: script}
		} else if cs.Uses != "" {
			// Nested uses inside composite – treat as builtin or JS for now
			// (recursive expansion is future work; for V1 we store as JS)
			ps.JS = &plan.JSStep{Repo: cs.Uses, Inputs: map[string]string{}}
			for k, v := range cs.With {
				// Map outer inputs
				for ok, ov := range outerInputs {
					v = strings.ReplaceAll(v, "${{ inputs."+ok+" }}", ov)
				}
				if ps.JS.Inputs == nil {
					ps.JS.Inputs = map[string]string{}
				}
				ps.JS.Inputs[k] = v
			}
		}
		out = append(out, ps)
	}
	return out, nil
}

func stDisplayName(st compiler.Step) string {
	if st.Name != "" {
		return st.Name
	}
	if st.Uses != nil {
		// `actions/checkout` → "checkout" keeps step ids short and stable.
		if _, base, ok := strings.Cut(st.Uses.Action, "/"); ok {
			return base
		}
		return st.Uses.Action
	}
	if st.RawUses != "" {
		if _, base, ok := strings.Cut(st.RawUses, "/"); ok {
			// Trim the @ref suffix for display
			if at := strings.LastIndex(base, "@"); at >= 0 {
				base = base[:at]
			}
			return base
		}
		return st.RawUses
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
