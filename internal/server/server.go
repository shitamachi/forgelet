// Package server exposes the control plane and reconciles durable work with Pods.
package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"

	"github.com/shitamachi/forgelet/internal/action"
	"github.com/shitamachi/forgelet/internal/blob"
	"github.com/shitamachi/forgelet/internal/delivery"
	"github.com/shitamachi/forgelet/internal/kube"
	gh "github.com/shitamachi/forgelet/internal/provider/github"
	"github.com/shitamachi/forgelet/internal/run"
	"github.com/shitamachi/forgelet/internal/storage"
	"github.com/shitamachi/forgelet/internal/webui"
	"github.com/shitamachi/forgelet/internal/workflow"
)

type Store interface {
	Management(context.Context) ([]byte, error)
	SaveManagement(context.Context, []byte, []byte) error
	PutSetting(context.Context, string, string, string, []byte) error
	Settings(context.Context, string, string) (map[string][]byte, error)
	DeleteSetting(context.Context, string, string, string) error
	PutEvent(context.Context, run.Event) (bool, error)
	PendingEvents(context.Context, ...time.Time) ([]run.Event, error)
	Events(context.Context) ([]run.Event, error)
	FinishEvent(context.Context, string, string) error
	CreateRun(context.Context, run.Run, []run.Plan) (bool, error)
	ListRunSummaries(context.Context) ([]run.Run, error)
	ListRuns(context.Context) ([]run.Run, error)
	GetRun(context.Context, string) (run.Run, error)
	GetJob(context.Context, string) (run.Job, error)
	Reserve(context.Context, int) ([]run.Job, error)
	RecordPod(context.Context, string, string) error
	Claim(context.Context, string, string) (run.Plan, error)
	Finish(context.Context, string, string, run.Result) error
	Interrupt(context.Context, string, string) error
	CancelRun(context.Context, string) error
	Active(context.Context) ([]run.Job, error)
	AppendLog(context.Context, string, string, run.Log) error
	Logs(context.Context, string, int64) ([]run.Log, error)
	PendingChecks(context.Context, ...time.Time) ([]run.Check, error)
	SaveCheck(context.Context, string, int64, run.State) error
	RetryRunChecks(context.Context, string) error
	RetryFailedEvent(context.Context, string) error
	ReplaceCheck(context.Context, string, int64, int64, run.State) error
	RetryEvent(context.Context, string, string, time.Time) error
	RetryCheck(context.Context, string, run.State, string, time.Time) error
	RetryRuntime(context.Context, string, string, bool, time.Time) error
	ClearRuntimeRetry(context.Context, string) error
	Backup(context.Context, string) error
	Prune(context.Context, time.Time, storage.RetentionPolicy, map[string]string) (storage.PruneResult, error)
}

type Runtime interface {
	Ensure(context.Context, run.Job, kube.Profile) (string, error)
	Observe(context.Context, run.Job) (kube.Observation, error)
	Delete(context.Context, run.Job) error
	Pods(context.Context) (map[string]string, error)
	Authenticate(context.Context, string) (kube.Identity, error)
}

type Provider interface {
	Workflows(context.Context, run.Event) (map[string][]byte, error)
	InstallationToken(context.Context, int64) (string, error)
	RepositoryToken(context.Context, int64, string) (string, error)
	ReportCheck(context.Context, run.Check, string) (int64, error)
}

type Config struct {
	PackageTokens    map[string]string
	GitHubAPIURL     string
	ActionResolver   *action.Resolver
	Auth             AuthConfig
	AdminAccess      string
	Targets          map[string]delivery.Target
	Delivery         *delivery.Coordinator
	Blobs            *blob.Store
	SecretKey        []byte
	Retention        Retention
	DiskSpace        DiskSpace
	AdminToken       string
	WebhookSecret    string
	PublicURL        string
	MaxParallel      int
	Repositories     []string
	Workflows        []string
	Profiles         map[string]kube.Profile
	RepositoryTokens RepositoryTokens
	DataDir          string
}

type Server struct {
	auth            *authRuntime
	managed         atomic.Pointer[Management]
	cfg             Config
	store           Store
	runtime         Runtime
	provider        Provider
	tickMu          sync.Mutex
	now             func() time.Time
	nextMaintenance time.Time
	disk            *diskGuard
}

func New(cfg Config, store Store, runtime Runtime, provider Provider) (*Server, error) {
	if err := validateGitHubAPIURL(cfg.GitHubAPIURL); err != nil {
		return nil, err
	}
	if cfg.ActionResolver != nil {
		copied := *cfg.ActionResolver
		cfg.ActionResolver = &copied
	}
	if err := validateRepositoryTokens(cfg.RepositoryTokens); err != nil {
		return nil, err
	}
	if err := validatePackageTokens(cfg.PackageTokens); err != nil {
		return nil, err
	}
	cfg.PackageTokens = maps.Clone(cfg.PackageTokens)
	bindings := RepositoryTokens{}
	for source, values := range cfg.RepositoryTokens {
		bindings[source] = map[string]string{}
		for name, target := range values {
			bindings[source][name] = target
		}
	}
	cfg.RepositoryTokens = bindings
	if err := validateDeliveryConfig(cfg); err != nil {
		return nil, err
	}
	if cfg.Retention == (Retention{}) {
		cfg.Retention = DefaultRetention()
	}
	if err := cfg.Retention.Validate(); err != nil {
		return nil, err
	}
	if cfg.DiskSpace == (DiskSpace{}) {
		cfg.DiskSpace = DefaultDiskSpace()
	}
	if err := cfg.DiskSpace.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.DataDir) == "" {
		return nil, errors.New("data_dir is required for disk space protection")
	}
	if len(cfg.SecretKey) != 0 && len(cfg.SecretKey) != 32 {
		return nil, errors.New("secret encryption key must be 32 bytes")
	}
	cfg.SecretKey = append([]byte(nil), cfg.SecretKey...)
	if cfg.AdminAccess == "" {
		cfg.AdminAccess = "token"
	}
	if cfg.AdminAccess != "open" && cfg.AdminAccess != "token" && cfg.AdminAccess != "session" {
		return nil, errors.New("admin_access must be open, token or session")
	}
	if cfg.AdminAccess != "open" && cfg.AdminAccess != "session" && len(cfg.AdminToken) < 16 {
		return nil, errors.New("admin token must have at least 16 characters")
	}
	if cfg.MaxParallel < 1 || cfg.MaxParallel > 1024 {
		return nil, errors.New("max_parallel must be between 1 and 1024")
	}
	if len(cfg.Profiles) == 0 {
		return nil, errors.New("at least one execution profile is required")
	}
	for name, p := range cfg.Profiles {
		if err := p.Validate(); err != nil {
			return nil, fmt.Errorf("profile %s: %w", name, err)
		}
	}
	if store == nil || runtime == nil {
		return nil, errors.New("store and runtime are required")
	}
	if cfg.PublicURL != "" {
		u, err := url.Parse(cfg.PublicURL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return nil, errors.New("invalid public URL")
		}
	}
	if cfg.AdminAccess == "session" {
		u, err := url.Parse(cfg.PublicURL)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && (u.Scheme != "http" || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1"))) {
			return nil, errors.New("session authentication requires an HTTPS public_url (loopback HTTP only for development)")
		}
	}
	cfg.Profiles = cloneProfiles(cfg.Profiles)
	return &Server{cfg: cfg, store: store, runtime: runtime, provider: provider, now: time.Now, disk: newDiskGuard(cfg.DataDir, cfg.DiskSpace)}, nil
}

func cloneProfiles(in map[string]kube.Profile) map[string]kube.Profile {
	out := make(map[string]kube.Profile, len(in))
	for k, v := range in {
		if v.Act != nil {
			copy := *v.Act
			copy.ImageMirrors = maps.Clone(v.Act.ImageMirrors)
			v.Act = &copy
		}
		if v.Build != nil {
			copy := *v.Build
			copy.Images = append([]string(nil), v.Build.Images...)
			v.Build = &copy
		}
		out[k] = v
	}
	return out
}

func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/api/auth/session", s.session)
	r.Post("/api/auth/login", s.login)
	r.Post("/api/auth/github/start", s.startGitHub)
	r.Get("/api/auth/github/callback", s.githubCallback)
	r.Post("/webhooks/github", s.webhook)
	r.Group(func(r chi.Router) {
		r.Use(s.admin)
		r.Post("/api/auth/logout", s.logout)
		r.Post("/api/auth/password", s.changePassword)
		r.Get("/api/github/user/repositories", s.githubUserRepositories)
		r.Post("/api/github/user/enable", s.githubUserEnable)
		r.Get("/api/config", s.consoleConfig)
		r.Get("/api/github/connection", s.githubConnection)
		r.Post("/api/github/enable", s.githubEnable)
		r.Post("/api/github/preview", s.githubPreview)
		r.Post("/api/github/run", s.githubRun)
		r.Get("/api/management", s.managementAPI)
		r.Put("/api/management", s.managementAPI)
		r.Post("/api/management/inspect", s.inspectWorkflows)
		r.Get("/api/status", s.status)
		r.Post("/api/runs/{runID}/checks/retry", func(w http.ResponseWriter, r *http.Request) {
			respond(w, map[string]bool{"retried": true}, s.store.RetryRunChecks(r.Context(), chi.URLParam(r, "runID")))
		})
		r.Post("/api/events/{deliveryID}/retry", func(w http.ResponseWriter, r *http.Request) {
			respond(w, map[string]bool{"retried": true}, s.store.RetryFailedEvent(r.Context(), chi.URLParam(r, "deliveryID")))
		})
		r.Get("/api/runs", func(w http.ResponseWriter, r *http.Request) {
			items, err := s.store.ListRunSummaries(r.Context())
			respond(w, items, err)
		})
		r.Get("/api/events", func(w http.ResponseWriter, r *http.Request) {
			items, err := s.store.Events(r.Context())
			for i := range items {
				items[i].Payload = nil
			}
			respond(w, items, err)
		})
		r.Get("/api/runs/{runID}", func(w http.ResponseWriter, r *http.Request) {
			item, err := s.store.GetRun(r.Context(), chi.URLParam(r, "runID"))
			respond(w, item, err)
		})
		r.Post("/api/runs", s.manual)
		r.Post("/api/runs/{runID}/cancel", func(w http.ResponseWriter, r *http.Request) {
			err := s.store.CancelRun(r.Context(), chi.URLParam(r, "runID"))
			respond(w, map[string]bool{"cancel_requested": err == nil}, err)
		})
		r.Post("/api/runs/{runID}/rerun", s.rerun)
		r.Get("/api/jobs/{jobID}/logs", s.logs)
		r.Get("/api/jobs/{jobID}/logs/download", s.downloadLogs)
		r.Get("/api/runs/{runID}/artifacts", s.artifacts)
		r.Get("/api/runs/{runID}/artifacts/{name}", s.artifacts)
		r.Get("/api/deployments", s.listDeliveries)
		r.Get("/api/deployments/{deploymentID}", s.getDelivery)
		r.Post("/api/deployments/{deploymentID}/rollback", s.rollback)
		r.Post("/api/backup", s.backup)
		r.Post("/api/maintenance", s.maintenance)
		r.Get("/api/repositories/{owner}/{repo}/{kind}", s.settings)
		r.Put("/api/repositories/{owner}/{repo}/{kind}/{name}", s.settings)
		r.Delete("/api/repositories/{owner}/{repo}/{kind}/{name}", s.settings)
	})
	r.Route("/internal/jobs/{jobID}", func(r chi.Router) {
		r.Post("/claim", s.claim)
		r.Get("/credentials", s.credentials)
		r.Post("/logs", s.appendLog)
		r.Post("/result", s.result)
		r.Get("/control", s.control)
		r.Put("/blobs/{kind}", s.runtimeBlob)
		r.Get("/blobs/{kind}", s.runtimeBlob)
		r.Post("/deployments", s.requestDelivery)
	})
	r.NotFound(webui.Handler().ServeHTTP)
	return r
}

func (s *Server) admin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.AdminAccess == "session" {
			if _, err := s.user(r); err != nil {
				http.Error(w, "authentication required", http.StatusUnauthorized)
				return
			}
			if !s.openManagement(w, r) {
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			next.ServeHTTP(w, r)
			return
		}
		if s.cfg.AdminAccess == "open" {
			if !s.openManagement(w, r) {
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			next.ServeHTTP(w, r)
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		// Basic auth is read-only, so browser ambient credentials cannot authorize mutations.
		if r.Method == http.MethodGet && !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			user, password, ok := r.BasicAuth()
			if ok && user == "admin" {
				token = password
			}
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.AdminToken)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="forgelet"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	if s.cfg.WebhookSecret == "" || s.provider == nil {
		http.Error(w, "webhook not configured", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "invalid or oversized body", http.StatusBadRequest)
		return
	}
	if !gh.VerifySignature(body, r.Header.Get("X-Hub-Signature-256"), s.cfg.WebhookSecret) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	eventName := r.Header.Get("X-GitHub-Event")
	if eventName == "ping" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "pong"})
		return
	}
	if eventName != "push" && eventName != "pull_request" {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "ignored event"})
		return
	}
	var e run.Event
	if eventName == "pull_request" {
		e, err = gh.ParsePullRequest(body)
	} else {
		e, err = gh.ParsePush(body)
	}
	if err != nil {
		if errors.Is(err, gh.ErrIgnored) {
			writeJSON(w, http.StatusAccepted, map[string]string{"status": "ignored push"})
			return
		}
		http.Error(w, "invalid event payload or missing merge commit", http.StatusBadRequest)
		return
	}
	e.DeliveryID = r.Header.Get("X-GitHub-Delivery")
	if len(e.DeliveryID) == 0 || len(e.DeliveryID) > 128 || !deliveryPattern.MatchString(e.DeliveryID) {
		http.Error(w, "invalid delivery ID", http.StatusBadRequest)
		return
	}
	if !s.allowed(e.Repository) {
		http.Error(w, "repository not enabled", http.StatusForbidden)
		return
	}
	created, err := s.store.PutEvent(r.Context(), e)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"delivery_id": e.DeliveryID, "created": created})
}

var ErrWorkflow = errors.New("invalid workflow")

var deliveryPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
var shaPattern = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)

func (s *Server) allowed(repo string) bool {
	if !repositoryPattern.MatchString(repo) {
		return false
	}
	for _, r := range s.management().Repositories {
		if r.Name == repo {
			return r.Enabled
		}
	}
	return false
}

func stableID(parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(digest[:12])
}
func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate run ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func (s *Server) manual(w http.ResponseWriter, r *http.Request) {
	var input struct {
		WorkflowFiles  map[string]string `json:"workflow_files"`
		EventName      string            `json:"event_name"`
		Inputs         map[string]any    `json:"inputs"`
		Files          []string          `json:"changed_files"`
		Repository     string            `json:"repository"`
		SHA            string            `json:"sha"`
		Ref            string            `json:"ref"`
		Workflow       string            `json:"workflow"`
		WorkflowPath   string            `json:"workflow_path"`
		InstallationID int64             `json:"installation_id"`
	}
	if err := decode(w, r, &input, 6<<20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.allowed(input.Repository) || !shaPattern.MatchString(input.SHA) || input.Workflow == "" {
		http.Error(w, "enabled repository, full SHA and workflow source are required", http.StatusBadRequest)
		return
	}
	if input.WorkflowPath == "" {
		input.WorkflowPath = ".github/workflows/manual.yml"
	}
	id, err := randomID()
	if err != nil {
		serverError(w, err)
		return
	}
	if input.EventName != "" && input.EventName != "push" && input.EventName != "workflow_dispatch" {
		http.Error(w, "manual event_name must be push or workflow_dispatch", 400)
		return
	}
	if input.Inputs != nil && input.EventName == "" {
		input.EventName = "workflow_dispatch"
	}
	event := run.Event{Name: input.EventName, Inputs: input.Inputs, Files: input.Files, FilesKnown: input.Files != nil, DeliveryID: "manual-" + id, Repository: input.Repository, SHA: input.SHA, Ref: input.Ref, InstallationID: input.InstallationID, Payload: json.RawMessage(`{}`)}
	var item run.Run
	if input.WorkflowFiles != nil {
		files := map[string][]byte{}
		for path, source := range input.WorkflowFiles {
			files[path] = []byte(source)
		}
		item, err = s.createRunFiles(r.Context(), event, input.WorkflowPath, []byte(input.Workflow), files)
	} else {
		item, err = s.createRun(r.Context(), event, input.WorkflowPath, []byte(input.Workflow))
	}
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

var errRunExpired = errors.New("run history expired")

// validatePlanSize keeps each claimed Plan below the executor's 1 MiB runtime
// response cap and bounds a single run's persisted JSON. Plan.Event/Vars are
// copied into every job, so a large webhook payload or matrix can otherwise
// create runs that can never be claimed and can grow SQLite without limit.
func validatePlanSize(plans []run.Plan) error {
	total := 0
	for _, plan := range plans {
		data, err := json.Marshal(plan)
		if err != nil {
			return fmt.Errorf("encode plan for job %q: %w", plan.Name, err)
		}
		if len(data) > run.MaxPlanBytes {
			return fmt.Errorf("job %q plan is %d bytes; limit is %d", plan.Name, len(data), run.MaxPlanBytes)
		}
		total += len(data)
		if total > run.MaxRunPlanBytes {
			return fmt.Errorf("workflow plans are %d bytes; limit is %d", total, run.MaxRunPlanBytes)
		}
	}
	return nil
}

func (s *Server) createRun(ctx context.Context, e run.Event, path string, source []byte) (run.Run, error) {
	files := s.sourceBundle(e.Repository, nil)
	if workflow.HasCalls(source) && s.provider != nil {
		var err error
		files, err = s.eventWorkflows(ctx, e)
		if err != nil {
			return run.Run{}, err
		}
		files = s.sourceBundle(e.Repository, files)
	}
	return s.createRunFiles(ctx, e, path, source, files)
}
func (s *Server) createRunFiles(ctx context.Context, e run.Event, path string, source []byte, files map[string][]byte) (run.Run, error) {
	// Manual admission has always defaulted to push in the workflow compiler.
	// Apply that default once here so policy binding uses the same event.
	if e.Name == "" {
		e.Name = "push"
	}
	e.WorkflowPath = path
	id := stableID(e.DeliveryID, path)
	if saved, err := s.store.GetRun(ctx, id); err == nil {
		return saved, nil
	} else if !errors.Is(err, storage.ErrNotFound) {
		return run.Run{}, err
	}
	vars, err := s.store.Settings(ctx, e.Repository, "vars")
	if err != nil {
		return run.Run{}, err
	}
	e.Vars = map[string]string{}
	for name, value := range vars {
		e.Vars[name] = string(value)
	}
	if len(e.Vars) == 0 {
		e.Vars = nil
	}
	plans, err := workflow.CompileFiles(source, e, id, files)
	if errors.Is(err, workflow.ErrFilesRequired) {
		if provider, ok := s.provider.(interface {
			ChangedFiles(context.Context, run.Event) ([]string, error)
		}); ok {
			e.Files, err = provider.ChangedFiles(ctx, e)
			if err != nil {
				if errors.Is(err, gh.ErrComparisonUnsupported) {
					return run.Run{}, fmt.Errorf("%w: %w", ErrWorkflow, err)
				}
				return run.Run{}, fmt.Errorf("load changed files: %w", err)
			}
			e.FilesKnown = true
			plans, err = workflow.CompileFiles(source, e, id, files)
		}
	}
	if err != nil {
		return run.Run{}, fmt.Errorf("%w: %w", ErrWorkflow, err)
	}
	if len(plans) == 0 {
		return run.Run{}, fmt.Errorf("%w: workflow has no runnable jobs", ErrWorkflow)
	}
	plans, err = s.attachDeployments(plans, e)
	if err != nil {
		return run.Run{}, fmt.Errorf("%w: %w", ErrWorkflow, err)
	}
	for i := range plans {
		plans[i].GitHubAPIURL = strings.TrimRight(s.cfg.GitHubAPIURL, "/")
	}
	if err := validatePlanSize(plans); err != nil {
		return run.Run{}, fmt.Errorf("%w: %w", ErrWorkflow, err)
	}
	if err := s.validateProfiles(plans); err != nil {
		return run.Run{}, fmt.Errorf("%w: %w", ErrWorkflow, err)
	}
	if err := s.resolveActions(ctx, plans); err != nil {
		return run.Run{}, err
	}
	if err := validatePlanSize(plans); err != nil {
		return run.Run{}, fmt.Errorf("%w: %w", ErrWorkflow, err)
	}
	if err := s.validateRequiredSecrets(ctx, e.Repository, plans); err != nil {
		return run.Run{}, fmt.Errorf("%w: %w", ErrWorkflow, err)
	}
	item := run.Run{ID: id, DeliveryID: e.DeliveryID, Repository: e.Repository, SHA: e.SHA, Ref: e.Ref, WorkflowPath: path, InstallationID: e.InstallationID, State: run.Queued, CreatedAt: time.Now().UTC()}
	created, err := s.store.CreateRun(ctx, item, plans)
	if err != nil {
		return run.Run{}, fmt.Errorf("save workflow: %w", err)
	}
	saved, err := s.store.GetRun(ctx, id)
	if !created && errors.Is(err, storage.ErrNotFound) {
		return run.Run{}, errRunExpired
	}
	return saved, err
}

func (s *Server) resolveActions(ctx context.Context, plans []run.Plan) error {
	resolver := action.Resolver{}
	if s.cfg.ActionResolver != nil {
		resolver = *s.cfg.ActionResolver
	}
	if err := resolver.Resolve(ctx, plans); err != nil {
		if errors.Is(err, action.ErrUnavailable) {
			return err
		}
		return fmt.Errorf("%w: %w", ErrWorkflow, err)
	}
	return nil
}

func (s *Server) validateProfiles(plans []run.Plan) error {
	for index := range plans {
		p := &plans[index]
		if p.Control != "" {
			continue
		}
		if p.Permissions != nil && p.Permissions["contents"] != "read" {
			for _, step := range p.Steps {
				if step.Checkout != nil || step.Action != nil && step.Action.Repository == "actions/checkout" {
					return fmt.Errorf("checkout requires contents: read")
				}
			}
		}
		profile, ok := s.cfg.Profiles[p.Profile]
		if !ok {
			return fmt.Errorf("unknown execution profile %q", p.Profile)
		}
		if err := s.packageAdmission(p, profile.Act != nil); err != nil {
			return err
		}
		if profile.Act != nil {
			p.Kernel = "act"
			for _, step := range p.Steps {
				if step.TimeoutSeconds > 0 {
					return fmt.Errorf("act profiles currently require job timeout; step timeout remains available on native profiles")
				}
				if step.Action != nil {
					uses := step.Action.Uses
					if strings.HasPrefix(uses, "forgelet/") || strings.Contains(uses, "artifact@") {
						return fmt.Errorf("profile %q does not support native forgelet/artifact adapters; use a native job", p.Profile)
					}
				}
			}
		} else {
			if p.Kernel == "act" {
				return fmt.Errorf("profile %q no longer enables act", p.Profile)
			}
			for _, step := range p.Steps {
				if step.Shell == "pwsh" {
					return fmt.Errorf("shell pwsh requires an act execution profile with PowerShell installed")
				}
				if step.Action == nil {
					continue
				}
				if action.ActVersions[step.Action.Uses] != "" && step.Action.Repository != "" || step.Action.Repository == "actions/checkout" {
					return fmt.Errorf("action %q requires an act execution profile", step.Action.Uses)
				}
				if err := action.ValidateNative(step.Action); err != nil {
					return err
				}
			}
		}
		if p.NeedsBuild() && profile.Build == nil {
			return fmt.Errorf("profile %q does not enable BuildKit", p.Profile)
		}
	}
	return nil
}

func (s *Server) rerun(w http.ResponseWriter, r *http.Request) {
	old, err := s.store.GetRun(r.Context(), chi.URLParam(r, "runID"))
	if err != nil {
		serverError(w, err)
		return
	}
	if !s.allowed(old.Repository) {
		http.Error(w, "repository disabled or removed", http.StatusForbidden)
		return
	}
	if !old.State.Terminal() {
		http.Error(w, "only completed runs can be rerun", http.StatusConflict)
		return
	}
	id, err := randomID()
	if err != nil {
		serverError(w, err)
		return
	}
	plans := make([]run.Plan, 0, len(old.Jobs))
	for _, j := range old.Jobs {
		p := j.Plan
		p.NeedsContext = nil
		p.CallValues = nil
		p.AncestorFailed = false
		p.RunID = id
		p.JobID = stableID(id, j.ID)
		plans = append(plans, p)
	}
	if err := s.validateProfiles(plans); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if err := s.validateRequiredSecrets(r.Context(), old.Repository, plans); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if err := validatePlanSize(plans); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	item := old
	item.ID = id
	item.DeliveryID = "rerun-" + id
	item.Jobs = nil
	item.State = run.Queued
	item.CreatedAt = time.Now().UTC()
	if _, err = s.store.CreateRun(r.Context(), item, plans); err != nil {
		serverError(w, err)
		return
	}
	item, err = s.store.GetRun(r.Context(), id)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) authenticate(r *http.Request) (run.Job, kube.Identity, error) {
	raw := r.Header.Get("Authorization")
	if !strings.HasPrefix(raw, "Bearer ") {
		return run.Job{}, kube.Identity{}, kube.ErrIdentity
	}
	identity, err := s.runtime.Authenticate(r.Context(), strings.TrimPrefix(raw, "Bearer "))
	if err != nil {
		return run.Job{}, kube.Identity{}, kube.ErrIdentity
	}
	job, err := s.store.GetJob(r.Context(), chi.URLParam(r, "jobID"))
	if err != nil {
		return run.Job{}, kube.Identity{}, kube.ErrIdentity
	}
	if job.ID != identity.JobID || job.PodName != identity.PodName || (job.PodUID != "" && job.PodUID != identity.PodUID) {
		return run.Job{}, kube.Identity{}, kube.ErrIdentity
	}
	return job, identity, nil
}

func (s *Server) claim(w http.ResponseWriter, r *http.Request) {
	job, who, err := s.authenticate(r)
	if err != nil || who.Terminating {
		http.Error(w, "invalid workload identity", http.StatusUnauthorized)
		return
	}
	p, err := s.store.Claim(r.Context(), job.ID, who.PodUID)
	respond(w, p, err)
}
func (s *Server) credentials(w http.ResponseWriter, r *http.Request) {
	job, who, err := s.authenticate(r)
	if err != nil || who.Terminating {
		http.Error(w, "invalid workload identity", http.StatusUnauthorized)
		return
	}
	if job.State != run.Running || job.CancelRequested {
		http.Error(w, "job is not running", http.StatusConflict)
		return
	}
	needed := job.Plan.Kernel == "act" && (job.Plan.Permissions == nil || job.Plan.Permissions["contents"] == "read")
	if job.Plan.Kernel != "act" {
		for _, step := range job.Plan.Steps {
			if step.Checkout != nil {
				needed = true
			}
		}
	}
	secrets, secretErr := s.runtimeSecrets(r.Context(), job.Plan)
	if secretErr != nil {
		http.Error(w, "job secrets unavailable", http.StatusServiceUnavailable)
		return
	}
	packageToken, packageErr := s.packageToken(r.Context(), job.Plan)
	if packageErr != nil {
		http.Error(w, "package credential unavailable", http.StatusServiceUnavailable)
		return
	}
	current, stateErr := s.store.GetJob(r.Context(), job.ID)
	if stateErr != nil {
		serverError(w, stateErr)
		return
	}
	if current.State != run.Running || current.CancelRequested || current.PodUID != who.PodUID {
		http.Error(w, "job is no longer running", http.StatusConflict)
		return
	}
	response := map[string]any{"secrets": secrets}
	if packageToken != "" {
		response["package_token"] = packageToken
	}
	if !needed {
		writeJSON(w, http.StatusOK, response)
		return
	}
	item, err := s.store.GetRun(r.Context(), job.RunID)
	if err != nil {
		serverError(w, err)
		return
	}
	token := ""
	if item.InstallationID != 0 {
		if s.provider == nil {
			http.Error(w, "provider unavailable", http.StatusServiceUnavailable)
			return
		}
		token, err = s.provider.RepositoryToken(r.Context(), item.InstallationID, item.Repository)
		if err != nil {
			http.Error(w, "clone credentials unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	current, stateErr = s.store.GetJob(r.Context(), job.ID)
	if stateErr != nil {
		serverError(w, stateErr)
		return
	}
	if current.State != run.Running || current.CancelRequested || current.PodUID != who.PodUID {
		http.Error(w, "job is no longer running", http.StatusConflict)
		return
	}
	response["clone_url"] = "https://github.com/" + item.Repository + ".git"
	response["clone_token"] = token
	writeJSON(w, http.StatusOK, response)
}
func (s *Server) appendLog(w http.ResponseWriter, r *http.Request) {
	job, who, err := s.authenticate(r)
	if err != nil {
		http.Error(w, "invalid workload identity", http.StatusUnauthorized)
		return
	}
	var entry run.Log
	if err = decode(w, r, &entry, 128<<10); err != nil {
		http.Error(w, "invalid log", http.StatusBadRequest)
		return
	}
	err = s.store.AppendLog(r.Context(), job.ID, who.PodUID, entry)
	respond(w, map[string]int64{"seq": entry.Seq}, err)
}
func (s *Server) result(w http.ResponseWriter, r *http.Request) {
	job, who, err := s.authenticate(r)
	if err != nil {
		http.Error(w, "invalid workload identity", http.StatusUnauthorized)
		return
	}
	var result run.Result
	if err = decode(w, r, &result, 1<<20); err != nil {
		http.Error(w, "invalid result", http.StatusBadRequest)
		return
	}
	err = s.store.Finish(r.Context(), job.ID, who.PodUID, result)
	respond(w, map[string]bool{"saved": err == nil}, err)
}
func (s *Server) control(w http.ResponseWriter, r *http.Request) {
	job, _, err := s.authenticate(r)
	if err != nil {
		http.Error(w, "invalid workload identity", http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"cancel_requested": job.CancelRequested || job.State.Terminal()})
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	after, err := strconv.ParseInt(defaultString(r.URL.Query().Get("after"), "0"), 10, 64)
	if err != nil || after < 0 {
		http.Error(w, "invalid log cursor", http.StatusBadRequest)
		return
	}
	id := chi.URLParam(r, "jobID")
	if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		items, err := s.store.Logs(r.Context(), id, after)
		respond(w, items, err)
		return
	}
	job, err := s.store.GetJob(r.Context(), id)
	if err != nil {
		serverError(w, err)
		return
	}
	if job.LogsExpired {
		serverError(w, storage.ErrLogsExpired)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		entries, err := s.store.Logs(r.Context(), id, after)
		if err != nil {
			return
		}
		for _, e := range entries {
			data, err := json.Marshal(e)
			if err != nil {
				return
			}
			if _, err = fmt.Fprintf(w, "id: %d\ndata: %s\n\n", e.Seq, data); err != nil {
				return
			}
			after = e.Seq
		}
		if _, err = io.WriteString(w, ": heartbeat\n\n"); err != nil {
			return
		}
		flusher.Flush()
		job, err := s.store.GetJob(r.Context(), id)
		if err != nil {
			return
		}
		if job.State.Terminal() && len(entries) == 0 {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) backup(w http.ResponseWriter, r *http.Request) {
	id, err := randomID()
	if err != nil {
		serverError(w, err)
		return
	}
	path := filepath.Join(s.cfg.DataDir, "backup-"+id+".db")
	if err = s.store.Backup(r.Context(), path); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"file": filepath.Base(path), "restore": "restore with scheduling paused; do not automatically resume old nonterminal jobs"})
}
func defaultString(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
func decode(w http.ResponseWriter, r *http.Request, target any, maxBytes int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return fmt.Errorf("decode request: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON value")
	}
	return nil
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func respond(w http.ResponseWriter, value any, err error) {
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}
func serverError(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	message := "operation failed"
	switch {
	case errors.Is(err, action.ErrUnavailable):
		code = http.StatusServiceUnavailable
		message = err.Error()
	case errors.Is(err, errRunExpired):
		code = http.StatusGone
		message = "run history expired"
	case errors.Is(err, storage.ErrLogsExpired):
		code = http.StatusGone
		message = "logs expired by retention policy"
	case errors.Is(err, storage.ErrNotFound):
		code = http.StatusNotFound
		message = "not found"
	case errors.Is(err, storage.ErrConflict):
		code = http.StatusConflict
		message = "state conflict"
	case errors.Is(err, delivery.ErrConflict):
		code = http.StatusConflict
		message = "deployment conflict"
	case errors.Is(err, storage.ErrLimit):
		code = http.StatusRequestEntityTooLarge
		message = "storage limit exceeded"
	}
	http.Error(w, message, code)
}

// Tick is serialized so a single control-plane process cannot over-reserve through overlapping scans.
// Reservation correctness also lives in the store transaction, not just this mutex.
func (s *Server) Tick(ctx context.Context) error {
	s.tickMu.Lock()
	defer s.tickMu.Unlock()
	var problems []error
	if err := s.disk.check(ctx, s.now()); err != nil {
		// Fail closed for admission, but still reconcile existing jobs and clean up.
		problems = append(problems, err)
	}
	if s.provider != nil {
		events, err := s.store.PendingEvents(ctx, s.now())
		if err != nil {
			return err
		}
		for _, e := range events {
			if !s.allowed(e.Repository) {
				if err := s.store.FinishEvent(ctx, e.DeliveryID, "repository disabled or removed"); err != nil {
					problems = append(problems, err)
				}
				continue
			}
			files, err := s.eventWorkflows(ctx, e)
			if err != nil {
				problems = append(problems, fmt.Errorf("load delivery %s: %w", e.DeliveryID, err))
				if retryErr := s.store.RetryEvent(ctx, e.DeliveryID, err.Error(), s.now()); retryErr != nil {
					problems = append(problems, retryErr)
				}
				continue
			}
			var diagnostics []string
			for _, w := range s.management().Workflows {
				if w.Repository == e.Repository && w.Enabled && w.Source == "" {
					if _, ok := files[w.Path]; !ok {
						diagnostics = append(diagnostics, w.Path+": registered Git workflow is absent at event SHA")
					}
				}
			}
			bundle := s.sourceBundle(e.Repository, files)
			files = s.workflowFiles(e.Repository, files)
			paths := make([]string, 0, len(files))
			for path := range files {
				paths = append(paths, path)
			}
			slices.Sort(paths)
			retry := false
			for _, path := range paths {
				if s.managed.Load() == nil && len(s.cfg.Workflows) > 0 && !slices.Contains(s.cfg.Workflows, path) {
					continue
				}
				if _, err = s.createRunFiles(ctx, e, path, files[path], bundle); err != nil {
					if errors.Is(err, errRunExpired) || errors.Is(err, workflow.ErrNotTriggered) {
						continue
					} else if errors.Is(err, ErrWorkflow) {
						diagnostics = append(diagnostics, path+": "+err.Error())
					} else {
						problems = append(problems, err)
						retry = true
					}
				}
			}
			if retry {
				if err := s.store.RetryEvent(ctx, e.DeliveryID, "workflow admission temporarily failed", s.now()); err != nil {
					problems = append(problems, err)
				}
				continue
			}
			if err = s.store.FinishEvent(ctx, e.DeliveryID, strings.Join(diagnostics, "\n")); err != nil {
				problems = append(problems, err)
			}
		}
	}
	active, err := s.store.Active(ctx)
	if err != nil {
		return err
	}
	for _, job := range active {

		if s.now().Before(job.RuntimeRetryAt) {
			continue
		}
		observation, err := s.runtime.Observe(ctx, job)
		if err != nil {
			problems = append(problems, s.runtimeFailure(ctx, job, err))
			continue
		}
		if job.RuntimeFailures > 0 && !observation.Missing && !job.CancelRequested {
			if err := s.store.ClearRuntimeRetry(ctx, job.ID); err != nil {
				problems = append(problems, err)
			}
		}
		if observation.Missing {
			if job.PodUID != "" || job.CancelRequested {
				if err = s.store.Interrupt(ctx, job.ID, "execution pod is absent"); err != nil {
					problems = append(problems, err)
				}
				continue
			}
			if err = s.start(ctx, job); err != nil {
				problems = append(problems, err)
			}
			continue
		}
		if job.PodUID == "" {
			if err = s.store.RecordPod(ctx, job.ID, observation.UID); err != nil {
				problems = append(problems, err)
				continue
			}
			job.PodUID = observation.UID
		}
		if job.CancelRequested {
			if err = s.runtime.Delete(ctx, job); err != nil {
				problems = append(problems, s.runtimeFailure(ctx, job, err))
			}
			continue
		}
		if observation.Phase == corev1.PodFailed || observation.Phase == corev1.PodSucceeded {
			if err = s.store.Interrupt(ctx, job.ID, "executor stopped without an acknowledged result: "+observation.Reason); err != nil {
				problems = append(problems, err)
			}
		}
	}
	limit := s.cfg.MaxParallel
	if s.disk.snapshot().Paused {
		limit = 0
	}
	jobs, err := s.store.Reserve(ctx, limit)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if err = s.start(ctx, job); err != nil {
			problems = append(problems, err)
		}
	}
	if s.provider != nil {
		checks, err := s.store.PendingChecks(ctx, s.now())
		if err != nil {
			return err
		}
		for _, check := range checks {
			if check.Run.InstallationID == 0 {
				continue
			}
			remoteID, err := s.provider.ReportCheck(ctx, check, s.checkURL(check.Run.ID))
			if err != nil {
				problems = append(problems, err)
				if retryErr := s.store.RetryCheck(ctx, check.Job.ID, check.Job.State, err.Error(), s.now()); retryErr != nil {
					problems = append(problems, retryErr)
				}
				continue
			}
			if check.RemoteID > 0 && check.RemoteID != remoteID {
				err = s.store.ReplaceCheck(ctx, check.Job.ID, check.RemoteID, remoteID, check.Job.State)
			} else {
				err = s.store.SaveCheck(ctx, check.Job.ID, remoteID, check.Job.State)
			}
			if err != nil {
				problems = append(problems, err)
			}
		}
	}
	pods, err := s.runtime.Pods(ctx)
	if err != nil {
		problems = append(problems, err)
	} else {
		for id, uid := range pods {
			job, err := s.store.GetJob(ctx, id)
			if err != nil || !job.State.Terminal() || job.PodUID != uid {
				continue
			}
			if err = s.runtime.Delete(ctx, job); err != nil {
				problems = append(problems, err)
			}
		}
	}
	if s.cfg.Delivery != nil {
		if err := s.cfg.Delivery.Tick(ctx); err != nil {
			problems = append(problems, err)
		}
	}
	if now := s.now(); !now.Before(s.nextMaintenance) {
		if _, err := s.maintain(ctx, now); err != nil {
			problems = append(problems, err)
		}
	}
	return errors.Join(problems...)
}
func (s *Server) start(ctx context.Context, job run.Job) error {
	// Also guard previously reserved attempts and pressure appearing mid-tick.
	// Observe runs first, so an already-created Pod is adopted even while paused.
	if err := s.disk.check(ctx, s.now()); err != nil {
		return err
	}
	if s.disk.snapshot().Paused {
		return nil
	}
	profile, ok := s.cfg.Profiles[job.Profile]
	if !ok {
		return s.store.Interrupt(ctx, job.ID, "execution profile no longer exists")
	}
	uid, err := s.runtime.Ensure(ctx, job, profile)
	if err != nil {
		return s.runtimeFailure(ctx, job, err)
	}
	return s.store.RecordPod(ctx, job.ID, uid)
}

func (s *Server) checkURL(id string) string {
	if s.cfg.PublicURL == "" {
		return ""
	}
	return strings.TrimRight(s.cfg.PublicURL, "/") + "/runs/" + id
}

func (s *Server) runtimeFailure(ctx context.Context, job run.Job, cause error) error {
	err := s.store.RetryRuntime(ctx, job.ID, cause.Error(), errors.Is(cause, kube.ErrPermanent), s.now())
	return errors.Join(fmt.Errorf("reconcile job %s: %w", job.ID, cause), err)
}
