// Command controller reconciles JobRun CRs into primary pods and projects
// observed state to the control plane (specs 0004 and 0011).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/shitamachi/forgelet/internal/runtime/controller"
)

func metricOpts(enabled bool) metricsserver.Options {
	if !enabled {
		return metricsserver.Options{BindAddress: "0"}
	}
	return metricsserver.Options{BindAddress: ":8080"}
}

func main() {
	var (
		apiURL      = flag.String("api-url", "http://forgelet-server.forgelet-system.svc", "control plane base URL")
		token       = flag.String("token", "", "control plane bearer token (required)")
		metricsFlag = flag.Bool("metrics", false, "enable controller-runtime metrics server (binds :8080)")
		jobsNS      = flag.String("jobs-namespace", "forgelet-jobs", "namespace whose JobRuns and pods are reconciled")
	)
	flag.Parse()

	if *token == "" {
		fmt.Fprintln(os.Stderr, "controller: -token is required")
		os.Exit(2)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	log.SetLogger(logr.FromSlogHandler(logger.Handler()))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: controller.NewScheme(),
		// The controller reconciles one namespace; the cache stays scoped so
		// the namespaced Role is sufficient (least privilege, 0011).
		Cache:                  cache.Options{DefaultNamespaces: map[string]cache.Config{*jobsNS: {}}},
		HealthProbeBindAddress: ":8081",
		Metrics:                metricOpts(*metricsFlag),
	})
	if err != nil {
		logger.Error("manager", "err", err.Error())
		os.Exit(1)
	}

	reconciler := &controller.JobRunReconciler{
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		Projection: controller.NewHTTPProjection(*apiURL, *token, nil),
		Now:        time.Now,
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		logger.Error("setup reconciler", "err", err.Error())
		os.Exit(1)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Error("health check", "err", err.Error())
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	_ = ctx // signal handling for Start is provided by SetupSignalHandler
	logger.Info("controller starting")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Error("controller stopped", "err", err.Error())
		os.Exit(1)
	}
}
