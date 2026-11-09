//go:build darwin || linux

package runner

import (
	"context"
	"os"
	goruntime "runtime"
	"time"

	"github.com/shitamachi/forgelet/internal/executor"
	"github.com/shitamachi/forgelet/internal/run"
)

// Native execution runs the job directly in this account, as every desktop
// backend did before WSLC. macOS uses it for Darwin jobs; Linux uses it for
// native Linux hosts and WSL2 distributions.
type nativeBackend struct{}

func newBackend(context.Context, Config, func(Status)) (backend, error) {
	return nativeBackend{}, nil
}

// describe reports the native backend: jobs run directly in this account.
func (nativeBackend) describe() descriptor { return descriptor{backend: "native"} }

func deviceLabels(Config) []string {
	if goruntime.GOOS == "darwin" {
		return []string{"macos-" + goruntime.GOARCH}
	}
	// There is no Linux desktop build; a Linux runner predates capability
	// reporting and keeps the original shared queue.
	return nil
}

func (nativeBackend) executeLease(ctx context.Context, c *client, workRoot string, grant run.ExternalGrant, token string, status func(Status)) Status {
	result := Status{State: "failed", Job: &grant}
	dir, err := os.MkdirTemp(workRoot, "attempt-")
	if err != nil {
		result.Message = "无法创建临时工作区"
		return result
	}
	defer func() { _ = os.RemoveAll(dir) }()
	var reported run.State
	err = executor.RunRemote(ctx, executor.RemoteOptions{URL: c.url.String(), JobID: grant.JobID, AttemptToken: token,
		Workspace: dir, Client: &c.http, RetryTimeout: 25 * time.Second, FlushTimeout: 8 * time.Second,
		OnResult: func(state run.State) { reported = state },
		OnLog:    func(entry run.Log) { status(Status{Log: &entry}) }})
	stopped := ctx.Err() != nil
	switch {
	case err == nil:
		result.State = "succeeded"
	case reported == run.Cancelled || stopped:
		result.State = "stopped"
		result.Message = "任务停止，最终状态请查看控制面"
	case reported == run.TimedOut:
		result.State = "timed_out"
		result.Message = "任务超时，详细日志请查看控制面"
	default:
		result.Message = "任务未成功，详细日志请查看控制面"
	}
	return result
}
