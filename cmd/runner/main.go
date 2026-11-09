// The desktop app keeps stdin open for the lifetime of this worker.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/shitamachi/forgelet/internal/runner"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	status := json.NewEncoder(os.Stdout)
	var err error
	if len(os.Args) > 1 && os.Args[1] == "--exec" {
		// Container mode: one job, configured on stdin, no polling.
		err = runExec(ctx, status, os.Stdin)
	} else {
		err = start(ctx, status)
	}
	if err != nil {
		_ = status.Encode(runner.Status{State: "error", Message: err.Error()})
		os.Exit(1)
	}
}

func start(ctx context.Context, status *json.Encoder) error {
	if err := checkPlatform(); err != nil {
		return err
	}
	input := bufio.NewScanner(os.Stdin)
	input.Buffer(make([]byte, 4096), 4096)
	if !input.Scan() {
		return errors.New("desktop connection settings are required")
	}
	var cfg runner.Config
	if err := json.Unmarshal(input.Bytes(), &cfg); err != nil {
		return errors.New("invalid desktop connection settings")
	}
	if err := prepareTLS(cfg); err != nil {
		return err
	}
	lifetime, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		for input.Scan() {
		}
		cancel()
	}()
	root, err := workRoot()
	if err != nil {
		return err
	}
	unlock, err := lockRoot(root)
	if err != nil {
		return err
	}
	defer unlock()
	return runner.Run(lifetime, cfg, root, func(state runner.Status) {
		if err := status.Encode(state); err != nil {
			cancel()
		}
	})
}

// workRoot is the account's private cache directory. Finder and Explorer can
// launch the app without a shell, so caches never depend on the calling
// environment.
func workRoot() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	root := filepath.Join(cache, "io.forgelet.runner")
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	if err := os.Setenv("GOCACHE", filepath.Join(root, "go-build")); err != nil {
		return "", err
	}
	if err := os.Setenv("GOMODCACHE", filepath.Join(root, "go-mod")); err != nil {
		return "", err
	}
	return root, nil
}
