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
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shitamachi/forgelet/internal/run/model"
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
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	engine := &executor.Engine{
		CP:      httpclient.New(*controlPlane, string(token), nil),
		WorkDir: *workDir,
		Grace:   *grace,
		Logger:  logger,
	}

	plan, err := engine.CP.FetchPlan(ctx, id)
	if err != nil {
		logger.Error("fetch plan", "err", err.Error())
		os.Exit(1)
	}
	result, err := engine.Run(ctx, id, plan)
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
