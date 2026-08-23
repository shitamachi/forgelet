// Command server is the forgelet control plane for M0: GitHub webhook
// ingestion, scheduling and the executor-facing internal API (spec 0011).
//
// M0 runs with an in-memory durable store, local identity tokens and a
// local workflow directory; those are ports swapped by the V1 tasks
// (PostgreSQL adapter, TokenReview verifier, GitHub content API).
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

	"github.com/shitamachi/forgelet/internal/report"
	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/run/scheduler"
	"github.com/shitamachi/forgelet/internal/server"
	"github.com/shitamachi/forgelet/internal/storage/postgres"
)

func main() {
	var (
		addr     = flag.String("addr", ":8080", "listen address")
		secret   = flag.String("webhook-secret", "", "GitHub webhook secret (required)")
		dir      = flag.String("workflows-dir", "./workflows", "workflow files directory (M0 local source)")
		dbURL    = flag.String("database-url", "", "PostgreSQL DSN; empty uses the in-memory store (dev only)")
		tokenKey = flag.String("token-key", "", "hex key for local executor identity (required)")
		details  = flag.String("details-base-url", "http://localhost:8080", "base URL for check details links")
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

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

	srv, err := server.NewServer(server.Options{
		Durable:        durable,
		WebhookSecret:  []byte(*secret),
		WorkflowsDir:   *dir,
		TokenKey:       key,
		DetailsBaseURL: *details,
		CheckReporter:  logReporter{logger},
		SecretValues:   map[string]string{},
		Log:            logger,
	})
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

// logReporter is the M0 development CheckReporter: it logs checks instead
// of calling GitHub (real adapter wiring is spec 0011 T7).
type logReporter struct{ log *slog.Logger }

func (l logReporter) Report(_ context.Context, _ model.RunRecord, c report.Check) error {
	l.log.Info("check", "job", c.Name, "externalId", c.ExternalID,
		"status", string(c.Status), "conclusion", string(c.Conclusion), "details", c.DetailsURL)
	return nil
}
