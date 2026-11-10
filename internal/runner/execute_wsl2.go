//go:build windows

package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/shitamachi/forgelet/internal/run"
	"github.com/shitamachi/forgelet/internal/wsl"
)

// The WSL2 backend runs the Linux executor natively inside a distribution, so
// the machine's own tooling - Docker included - is used as a shell would use it.
// That makes it the better choice when a job builds images or starts containers.
type wsl2Backend struct {
	client     wsl.Client
	distro     string
	user       string
	runner     string
	engine     string
	dockerHost string
}

const (
	wsl2StartTimeout = 3 * time.Minute
	wsl2StopGrace    = 20 * time.Second
	wsl2StopWait     = 25 * time.Second
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
	engine := ""
	if version := client.DockerVersion(start, cfg.WSL2.Distro, cfg.WSL2.User); version != "" {
		engine = "Docker " + version
	}
	dockerHost := client.DockerHost(start, cfg.WSL2.Distro, cfg.WSL2.User)
	if engine != "" {
		status(Status{State: "preparing", Message: "已连接 WSL2：" + engine})
	}
	return &wsl2Backend{client: client, distro: cfg.WSL2.Distro, user: cfg.WSL2.User, runner: runnerPath,
		engine: engine, dockerHost: dockerHost}, nil
}

func (b *wsl2Backend) describe() descriptor {
	return descriptor{backend: "wsl2", engine: b.engine}
}

func (b *wsl2Backend) executeLease(ctx context.Context, c *client, _ string, grant run.ExternalGrant, token string, status func(Status)) Status {
	result := Status{State: "failed", Job: &grant}
	request := ExecRequest{URL: c.url.String(), JobID: grant.JobID, Attempt: token}
	if b.dockerHost != "" {
		request.Env = map[string]string{"DOCKER_HOST": b.dockerHost}
	}
	payload, err := json.Marshal(request)
	if err != nil {
		result.Message = "无法编码执行配置"
		return result
	}
	stdin, stdinWriter, err := os.Pipe()
	if err != nil {
		result.Message = "无法创建执行器输入管道"
		return result
	}
	defer func() { _ = stdin.Close() }()
	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		result.Message = "无法创建执行器输出管道"
		return result
	}
	defer func() { _ = stdout.Close() }()
	stderr := &tailWriter{limit: 4096}
	command := b.client.Command(context.Background(), b.client.RunArgs(b.distro, b.user, b.runner)...)
	command.Stdin = stdin
	command.Stdout = stdoutWriter
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		_ = stdinWriter.Close()
		_ = stdoutWriter.Close()
		result.Message = "无法启动 WSL2 执行器"
		return result
	}
	_ = stdoutWriter.Close()
	var closeInput sync.Once
	closeStdin := func() {
		closeInput.Do(func() { _ = stdinWriter.Close() })
	}
	finished := make(chan struct{})
	go func() {
		if _, err := stdinWriter.Write(append(payload, '\n')); err != nil {
			return
		}
		select {
		case <-ctx.Done():
		case <-finished:
		}
		closeStdin()
	}()
	go func() {
		select {
		case <-ctx.Done():
		case <-finished:
			return
		}
		closeStdin()
		timer := time.NewTimer(wsl2StopGrace)
		defer timer.Stop()
		select {
		case <-finished:
			return
		case <-timer.C:
		}
		// Closing stdin is the protocol's stop signal; killing the client is the
		// last resort when the distribution does not react in time.
		select {
		case <-finished:
		case <-time.After(wsl2StopWait):
			_ = command.Process.Kill()
		}
	}()
	var mu sync.Mutex
	var reported run.State
	var message string
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			var update struct {
				State   string   `json:"state"`
				Message string   `json:"message"`
				Log     *run.Log `json:"log"`
			}
			if json.Unmarshal(scanner.Bytes(), &update) != nil {
				continue
			}
			if update.Log != nil {
				status(Status{Log: update.Log})
			}
			if update.State == "" {
				continue
			}
			mu.Lock()
			reported = run.State(update.State)
			message = update.Message
			state := reported
			mu.Unlock()
			if jobTerminal(string(state)) {
				status(Status{State: DesktopState(state), Message: update.Message, Job: &grant})
			}
		}
	}()
	waitErr := command.Wait()
	close(finished)
	closeStdin()
	<-readerDone
	mu.Lock()
	state, detail := reported, message
	mu.Unlock()
	if jobTerminal(string(state)) {
		result.State = DesktopState(state)
		result.Message = detail
	} else if ctx.Err() != nil {
		result.State = "stopped"
		result.Message = "任务停止，最终状态请查看控制面"
	} else {
		result.State = "failed"
		result.Message = "任务未成功，详细日志请查看控制面"
		if detail != "" {
			result.Message = "任务未成功：" + firstLine(detail)
		} else if text := stderr.Meaningful(); text != "" {
			result.Message = "任务未成功：" + text
		} else if waitErr != nil {
			result.Message = fmt.Sprintf("wsl 退出：%v", waitErr)
		}
	}
	return result
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if strings.EqualFold(strings.TrimSpace(candidate), value) {
			return true
		}
	}
	return false
}
