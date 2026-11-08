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
	"runtime"
	"syscall"

	"github.com/shitamachi/forgelet/internal/runner"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	status := json.NewEncoder(os.Stdout)
	if err := start(ctx, status); err != nil {
		_ = status.Encode(runner.Status{State: "error", Message: err.Error()})
		os.Exit(1)
	}
}

func start(ctx context.Context, status *json.Encoder) error {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return errors.New("this runner requires an Apple Silicon Mac")
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
	lifetime, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		for input.Scan() {
		}
		cancel()
	}()
	// Finder launches without a shell environment. Use only known tool directories.
	if err := os.Setenv("PATH", "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"); err != nil {
		return err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	root := filepath.Join(cache, "io.forgelet.runner")
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	// Finder can inherit development caches on volumes the app cannot access.
	if err := os.Setenv("GOCACHE", filepath.Join(root, "go-build")); err != nil {
		return err
	}
	if err := os.Setenv("GOMODCACHE", filepath.Join(root, "go-mod")); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(root, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("a Forgelet desktop runner is already active on this account")
	}
	return runner.Run(lifetime, cfg, root, func(state runner.Status) {
		if err := status.Encode(state); err != nil {
			cancel()
		}
	})
}
