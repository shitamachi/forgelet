//go:build linux

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shitamachi/forgelet/internal/executor"
	"github.com/shitamachi/forgelet/internal/run"
	"github.com/shitamachi/forgelet/internal/runner"
)

// validEnvKey keeps supervisor-provided names to what a shell can carry.
func validEnvKey(key string) bool {
	if key == "" || len(key) > 64 {
		return false
	}
	for _, r := range key {
		if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// runExec is the container side of a Windows job: it runs exactly one attempt
// inside the Linux container and reports its logs and final state on stdout.
// Closing stdin (the desktop app's stop signal) cancels the attempt.
func runExec(ctx context.Context, status *json.Encoder, input io.Reader) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 64<<10)
	if !scanner.Scan() {
		return errors.New("缺少执行配置")
	}
	var request runner.ExecRequest
	if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
		return errors.New("执行配置格式无效")
	}
	if request.JobID == "" || run.ExternalAttempt(request.Attempt) == "" {
		return errors.New("执行配置缺少任务或工作区")
	}
	// The executor gives each job its own HOME, so a distribution's Docker
	// context is not inherited automatically; the supervisor states the daemon
	// endpoint explicitly.
	for key, value := range request.Env {
		if !validEnvKey(key) || strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("执行配置包含无效环境变量")
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	// A container passes its workspace explicitly; native WSL2 execution lets the
	// runner create a private directory under the account's temp area.
	workspace := request.Workspace
	if workspace == "" {
		dir, err := os.MkdirTemp("", "forgelet-job-")
		if err != nil {
			return errors.New("无法创建任务工作区")
		}
		defer func() { _ = os.RemoveAll(dir) }()
		workspace = dir
	}
	if request.CACert != "" {
		dir, err := os.MkdirTemp("", "forgelet-ca-")
		if err != nil {
			return errors.New("无法创建证书临时目录")
		}
		defer func() { _ = os.RemoveAll(dir) }()
		path := filepath.Join(dir, "ca.pem")
		if err := os.WriteFile(path, []byte(request.CACert), 0600); err != nil {
			return errors.New("无法写入容器 CA 证书")
		}
		// Go's HTTP client honours SSL_CERT_FILE; the image keeps its own roots.
		if err := os.Setenv("SSL_CERT_FILE", path); err != nil {
			return err
		}
	}
	lifetime, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		for scanner.Scan() {
		}
		cancel()
	}()
	var mu sync.Mutex
	report := func(update runner.Status) {
		mu.Lock()
		defer mu.Unlock()
		_ = status.Encode(update)
	}
	var final run.State
	err := executor.RunRemote(lifetime, executor.RemoteOptions{
		URL: request.URL, JobID: request.JobID, AttemptToken: request.Attempt,
		Workspace: workspace, RetryTimeout: 25 * time.Second, FlushTimeout: 8 * time.Second,
		OnResult: func(state run.State) {
			mu.Lock()
			final = state
			mu.Unlock()
			report(runner.Status{State: string(state)})
		},
		OnLog: func(entry run.Log) { report(runner.Status{Log: &entry}) },
	})
	mu.Lock()
	state := final
	mu.Unlock()
	if err != nil && (lifetime.Err() != nil || state == run.Cancelled || state == run.Interrupted) {
		// A stop, or a cancellation this container noticed first: the control
		// plane owns the verdict, so this is not a runner failure.
		report(runner.Status{State: "stopped"})
		return nil
	}
	return err
}
