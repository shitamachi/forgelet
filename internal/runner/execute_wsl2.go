//go:build windows

package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/shitamachi/forgelet/internal/run"
	"github.com/shitamachi/forgelet/internal/wsl"
)

// The WSL2 backend runs the Linux executor natively inside a distribution, so
// the machine's own tooling - Docker included - is used as a shell would use it.
// That makes it the better choice when a job builds images or starts containers.
type wsl2Backend struct {
	client       wsl.Client
	distro       string
	user         string
	runner       string
	act          string
	engine       string
	dockerHost   string
	dockerConfig string
}

const (
	wsl2StartTimeout = 3 * time.Minute
)

func newWSL2Backend(ctx context.Context, cfg Config, status func(Status)) (backend, error) {
	if cfg.WSL2 == nil {
		return nil, errors.New("WSL2 后端缺少配置")
	}
	if cfg.WSL2.RunnerBinary == "" {
		return nil, errors.New("WSL2 后端缺少内置 Linux 执行器")
	}
	path, err := wsl.Find()
	if err != nil {
		return nil, err
	}
	client := wsl.Client{Path: path}
	start, cancel := context.WithTimeout(ctx, wsl2StartTimeout)
	defer cancel()
	if cfg.WSL2.Distro != "" {
		distros, err := client.Distros(start)
		if err != nil {
			return nil, err
		}
		if !contains(distros, cfg.WSL2.Distro) {
			return nil, fmt.Errorf("WSL 发行版 %q 不存在，已安装: %s", cfg.WSL2.Distro, strings.Join(distros, ", "))
		}
	}
	home, err := client.Home(start, cfg.WSL2.Distro, cfg.WSL2.User)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(cfg.WSL2.RunnerBinary)
	if err != nil {
		return nil, fmt.Errorf("读取内置 Linux 执行器: %w", err)
	}
	runnerPath, err := client.InstallRunner(start, cfg.WSL2.Distro, cfg.WSL2.User, home, cfg.WSL2.RunnerBinary, info.Size())
	if err != nil {
		return nil, err
	}
	// The full act sandbox copies both binaries into itself with the
	// distribution's Docker CLI, so the act worker lives in the distribution too.
	actPath := ""
	if cfg.WSL2.ActBinary != "" {
		actInfo, err := os.Stat(cfg.WSL2.ActBinary)
		if err != nil {
			return nil, fmt.Errorf("读取内置 act 执行器: %w", err)
		}
		actPath, err = client.InstallFile(start, cfg.WSL2.Distro, cfg.WSL2.User, home, "forgelet-act", cfg.WSL2.ActBinary, actInfo.Size())
		if err != nil {
			return nil, err
		}
	}
	engine := ""
	if version := client.DockerVersion(start, cfg.WSL2.Distro, cfg.WSL2.User); version != "" {
		engine = "Docker " + version
	}
	dockerHost := client.DockerHost(start, cfg.WSL2.Distro, cfg.WSL2.User)
	if engine != "" {
		status(Status{State: "preparing", Message: "已连接 WSL2：" + engine})
	}
	return &wsl2Backend{client: client, distro: cfg.WSL2.Distro, user: cfg.WSL2.User, runner: runnerPath,
		act: actPath, engine: engine, dockerHost: dockerHost, dockerConfig: home + "/.docker"}, nil
}

// containerBinaries reports the executor paths the distribution's Docker CLI can
// copy from: host paths do not exist there.
func (b *wsl2Backend) containerBinaries() (string, string, bool) {
	if b.runner == "" || b.act == "" {
		return "", "", false
	}
	return b.runner, b.act, true
}

func (b *wsl2Backend) describe() descriptor {
	return descriptor{backend: "wsl2", engine: b.engine}
}

func (b *wsl2Backend) buildPlatforms(ctx context.Context) string {
	if b.dockerHost == "" {
		return "none"
	}
	return probeBuildPlatforms(ctx, b.dockerCommand())
}

func (b *wsl2Backend) dockerCommand() dockerCommand {
	if b.dockerHost == "" {
		return nil
	}
	return func(ctx context.Context, args ...string) *exec.Cmd {
		return b.client.Docker(ctx, b.distro, b.user, b.dockerHost, b.dockerConfig, args...)
	}
}

func (b *wsl2Backend) executeLease(ctx context.Context, c *client, _ string, grant run.ExternalGrant, token string, status func(Status)) Status {
	request := ExecRequest{URL: c.url.String(), JobID: grant.JobID, Attempt: token}
	if b.dockerHost != "" {
		request.Env = map[string]string{
			"DOCKER_HOST": b.dockerHost, "DOCKER_CONTEXT": "", "BUILDX_BUILDER": "",
			"FORGELET_RUNNER_DOCKER_CONFIG": b.dockerConfig,
		}
	}
	command := b.client.Command(context.WithoutCancel(ctx), b.client.RunArgs(b.distro, b.user, b.runner)...)
	return executeChild(ctx, command, request, grant, status, nil)
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if strings.EqualFold(strings.TrimSpace(candidate), value) {
			return true
		}
	}
	return false
}
