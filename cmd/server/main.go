// Command server is the forgelet control plane: GitHub webhook ingestion,
// scheduling, the executor-facing internal API and check reporting.
//
// Two provider modes:
//   - GitHub mode (any -github-* credential flag set): workflows are read
//     from the repository through the content API and checks are reported
//     as real Check Runs (spec 0005/0011).
//   - Dev mode (default): a local workflow directory and log-only checks;
//     the durable store falls back to memory unless -database-url is set.
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/shitamachi/forgelet/internal/observability/metrics"
	"github.com/shitamachi/forgelet/internal/observability/tracing"
	"github.com/shitamachi/forgelet/internal/provider/github"
	"github.com/shitamachi/forgelet/internal/report"
	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/run/scheduler"
	"github.com/shitamachi/forgelet/internal/security/identity"
	"github.com/shitamachi/forgelet/internal/security/tokenreview"
	"github.com/shitamachi/forgelet/internal/server"
	"github.com/shitamachi/forgelet/internal/storage/postgres"
)

func main() {
	var (
		addr     = flag.String("addr", ":8080", "listen address")
		secret   = flag.String("webhook-secret", "", "GitHub webhook secret (required)")
		dir      = flag.String("workflows-dir", "./workflows", "workflow files directory (dev source)")
		dbURL    = flag.String("database-url", "", "PostgreSQL DSN; empty uses the in-memory store (dev only)")
		tokenKey = flag.String("token-key", "", "hex key for local executor identity (required)")
		details  = flag.String("details-base-url", "http://localhost:8080", "base URL for check details links")

		apiBaseURL     = flag.String("github-api-base-url", github.DefaultBaseURL, "GitHub API base URL (GitHub Enterprise override)")
		ghToken        = flag.String("github-token", "", "static GitHub token enabling GitHub mode")
		appID          = flag.Int64("github-app-id", 0, "GitHub App id (with -github-installation-id/-github-app-key)")
		installationID = flag.Int64("github-installation-id", 0, "GitHub App installation id")
		appKeyPath     = flag.String("github-app-key", "", "GitHub App private key PEM path")
		scheduledRepos = flag.String("scheduled-repos", "", "comma-separated owner/name repos whose default branches are cron-scanned")

		executorAuth = flag.String("executor-auth", "local", "executor identity verification: local (HMAC dev tokens) or tokenreview (projected ServiceAccount tokens)")
		kubeconfig   = flag.String("kubeconfig", "", "kubeconfig path for TokenReview; empty uses in-cluster config")
		otlpEndpoint = flag.String("otlp-endpoint", "", "OTLP HTTP collector host:port for tracing; empty disables tracing")
	)
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if *secret == "" || *tokenKey == "" {
		fmt.Fprintln(os.Stderr, "server: -webhook-secret and -token-key are required")
		os.Exit(2)
	}
	key, err := hex.DecodeString(*tokenKey)
	if err != nil || len(key) < 32 {
		fmt.Fprintln(os.Stderr, "server: -token-key must be at least 32 bytes of hex")
		os.Exit(2)
	}

	tokens, err := githubTokens(*ghToken, *appID, *installationID, *appKeyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "server: %v\n", err)
		os.Exit(2)
	}
	repos, err := parseRepos(*scheduledRepos)
	if err != nil {
		fmt.Fprintf(os.Stderr, "server: %v\n", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	tp, shutdown, err := tracing.Setup(ctx, *otlpEndpoint, "forgelet-server")
	if err != nil {
		logger.Error("tracing", "err", err.Error())
		os.Exit(1)
	}
	defer func() { _ = shutdown(context.Background()) }()
	if *otlpEndpoint != "" {
		logger.Info("tracing: exporting to " + *otlpEndpoint)
	}

	var durable scheduler.DurableStore
	if *dbURL != "" {
		durable, err = postgres.New(ctx, *dbURL, nil, nil)
		if err != nil {
			logger.Error("postgres", "err", err.Error())
			os.Exit(1)
		}
		logger.Info("durable store: postgresql")
	} else {
		logger.Warn("durable store: in-memory (dev only)")
	}

	opts := server.Options{
		Durable:        durable,
		Metrics:        metrics.New(),
		TracerProvider: tp,
		WebhookSecret:  []byte(*secret),
		WorkflowsDir:   *dir,
		ScheduledRepos: repos,
		TokenKey:       key,
		DetailsBaseURL: *details,
		SecretValues:   map[string]string{},
		Log:            logger,
	}
	if *executorAuth == "tokenreview" {
		verifier, err := tokenReviewVerifier(*kubeconfig)
		if err != nil {
			logger.Error("tokenreview verifier", "err", err.Error())
			os.Exit(1)
		}
		opts.Verifier = verifier
		logger.Info("executor auth: tokenreview", "audience", identity.Audience, "namespace", jobNamespace)
	}
	if tokens != nil {
		hc := &http.Client{Transport: tracing.Transport(nil, tp), Timeout: 30 * time.Second}
		opts.WorkflowFetcher = githubSource{github.NewContentClient(*apiBaseURL, hc, tokens)}
		opts.CheckReporter = github.NewCheckReporter(*apiBaseURL, hc, tokens)
		logger.Info("provider: github", "auth", tokenAuthMode(*ghToken, *appID), "scheduledRepos", len(repos))
	} else {
		if len(repos) > 0 {
			logger.Warn("scheduled-repos without GitHub credentials scan the local workflows dir (dev only)")
		}
		opts.CheckReporter = logReporter{logger}
		logger.Info("provider: local dev (no -github-* flags)")
	}

	srv, err := server.NewServer(opts)
	if err != nil {
		logger.Error("wire server", "err", err.Error())
		os.Exit(1)
	}

	srv.StartLoops(ctx)

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		// Shutdown inherits the signal context but bounded in time.
		shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()
	logger.Info("server listening", "addr", *addr)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("http server", "err", err.Error())
		os.Exit(1)
	}
}

// logReporter is the dev-mode CheckReporter: it logs checks instead of
// calling GitHub; GitHub mode wires the real Check Run adapter.
type logReporter struct{ log *slog.Logger }

func (l logReporter) Report(_ context.Context, _ model.RunRecord, c report.Check) error {
	l.log.Info("check", "job", c.Name, "externalId", c.ExternalID,
		"status", string(c.Status), "conclusion", string(c.Conclusion), "details", c.DetailsURL)
	return nil
}

// githubTokens resolves the provider credential from the flags: either a
// static token or full App auth, never both (spec 0005 FR-G2).
func githubTokens(token string, appID, installationID int64, keyPath string) (github.TokenSource, error) {
	switch {
	case token != "" && (appID != 0 || installationID != 0 || keyPath != ""):
		return nil, fmt.Errorf("set either -github-token or -github-app-id/-github-installation-id/-github-app-key, not both")
	case token != "":
		return staticToken(token), nil
	case appID != 0 || installationID != 0 || keyPath != "":
		if appID == 0 || installationID == 0 || keyPath == "" {
			return nil, fmt.Errorf("-github-app-id, -github-installation-id and -github-app-key are required together")
		}
		appKey, err := loadAppKey(keyPath)
		if err != nil {
			return nil, err
		}
		return github.NewAppAuth(appID, installationID, appKey, "", nil, nil), nil
	default:
		return nil, nil
	}
}

func tokenAuthMode(token string, appID int64) string {
	if token != "" {
		return "static-token"
	}
	return "github-app"
}

// jobNamespace is the namespace executor pods run in (0004 §4); it matches
// the namespace minted identities carry.
const jobNamespace = "forgelet-jobs"

// tokenReviewVerifier builds the production executor identity verifier:
// projected ServiceAccount tokens are checked through the TokenReview API
// and bound to JobRuns via the pod label (spec 0003 FR-S1.5).
func tokenReviewVerifier(kubeconfig string) (identity.Verifier, error) {
	var cfg *rest.Config
	if kubeconfig != "" {
		c, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("load kubeconfig: %w", err)
		}
		cfg = c
	} else {
		c, err := rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("in-cluster config: %w (run inside the cluster or pass -kubeconfig)", err)
		}
		cfg = c
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kubernetes client: %w", err)
	}
	return &tokenreview.Verifier{
		Client:    client,
		Audience:  identity.Audience,
		Namespace: jobNamespace,
		SAName:    "forgelet-executor",
		Scopes:    []string{identity.ScopePlanRead, identity.ScopeSecretsRead, identity.ScopeStatusWrite},
		Bindings:  tokenreview.NewPodLabelBindings(client),
	}, nil
}
