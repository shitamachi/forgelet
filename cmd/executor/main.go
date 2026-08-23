// Command executor is the forgelet job executor: the future PID 1 of every
// primary job pod (spec 0008). It fetches its plan with a JobRun-scoped,
// audience-bound token, runs the steps and reports the result.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shitamachi/forgelet/internal/observability/tracing"
	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/run/plan"
	"github.com/shitamachi/forgelet/internal/runtime/executor"
	"github.com/shitamachi/forgelet/internal/runtime/executor/httpclient"
	"github.com/shitamachi/forgelet/internal/security/identity"
)

func main() {
	var (
		controlPlane = flag.String("control-plane", "http://forgelet-server.forgelet-system.svc", "control plane base URL")
		jobRunID     = flag.String("jobrun", "", "forgelet job run id (required)")
		tokenFile    = flag.String("token-file", "/var/run/forgelet/token", "workload identity token file")
		workDir      = flag.String("workdir", "/workspace", "workspace directory")
		timeout      = flag.Duration("timeout", time.Hour, "overall job timeout")
		grace        = flag.Duration("grace", executor.DefaultGrace, "SIGTERM to SIGKILL grace period")
		otlpEndpoint = flag.String("otlp-endpoint", "", "OTLP HTTP collector host:port for tracing; empty disables tracing")
	)
	flag.Parse()

	if *jobRunID == "" {
		fmt.Fprintln(os.Stderr, "executor: -jobrun is required")
		os.Exit(2)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	token, err := os.ReadFile(*tokenFile)
	if err != nil {
		logger.Error("read token", "err", err.Error())
		os.Exit(1)
	}

	// The token's claims bind audience, namespace, pod, job run and scopes;
	// the identity value here mirrors them for local logging only.
	id := identity.Identity{
		Audience: identity.Audience,
		JobRunID: model.JobRunID(*jobRunID),
		Scopes:   []string{identity.ScopePlanRead, identity.ScopeSecretsRead, identity.ScopeStatusWrite},
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	tp, shutdown, terr := tracing.Setup(ctx, *otlpEndpoint, "forgelet-executor")
	if terr != nil {
		logger.Error("tracing", "err", terr.Error())
		os.Exit(1)
	}
	defer func() { _ = shutdown(context.Background()) }()

	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	cpCtx, cpSpan := tp.Tracer("github.com/shitamachi/forgelet/cmd/executor").Start(ctx, "executor.job")
	defer cpSpan.End()
	engine := &executor.Engine{
		CP:      httpclient.New(*controlPlane, string(token), &http.Client{Transport: tracing.Transport(nil, tp)}),
		WorkDir: *workDir,
		Grace:   *grace,
		Logger:  logger,
	}

	// Cluster DNS can be briefly unavailable right after pod start; the
	// plan fetch is the one call worth retrying (idempotent read). Permanent
	// rejections (4xx) are not retried.
	var fetched plan.Plan
	var ferr error
	backoff := time.Second
	for attempt := 1; attempt <= 10; attempt++ {
		fetched, ferr = engine.CP.FetchPlan(cpCtx, id)
		if ferr == nil {
			break
		}
		var cerr *httpclient.ClientError
		if cpCtx.Err() != nil || (errors.As(ferr, &cerr) && !cerr.Retryable()) {
			break
		}
		logger.Warn("fetch plan retry", "attempt", attempt, "err", ferr.Error())
		select {
		case <-cpCtx.Done():
		case <-time.After(backoff):
		}
		if backoff < 15*time.Second {
			backoff *= 2
		}
	}
	if ferr != nil {
		logger.Error("fetch plan", "err", ferr.Error())
		os.Exit(1)
	}
	result, err := engine.Run(cpCtx, id, fetched)
	logger.Info("job finished", "success", result.Success, "cancelled", result.Cancelled, "steps", len(result.Steps))
	switch {
	case err == nil:
		os.Exit(0)
	case errors.Is(err, executor.ErrCancelled):
		os.Exit(130)
	default:
		os.Exit(1)
	}
}
