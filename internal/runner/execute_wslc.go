//go:build windows

package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/shitamachi/forgelet/internal/run"
	"github.com/shitamachi/forgelet/internal/wslc"
)

// The WSLC backend gives every job attempt one Linux container. The Windows
// process only supervises the CLI; the job itself runs in the container, where
// runner.os is Linux and runner.arch follows the image platform.
type wslcBackend struct {
	path    string
	version string
}

const (
	// stopGrace is how long a cancelled job may shut down inside the container
	// before the runner asks wslc to stop it.
	stopGrace = 20 * time.Second
	// stopWait is how long the CLI may take to stop or kill the container.
	stopWait = 25 * time.Second
)

// newBackend picks the execution environment this device is configured for.
// WSL2 runs natively in a distribution (Docker available); WSLC gives each
// attempt a fresh container.
func newBackend(ctx context.Context, cfg Config, status func(Status)) (backend, error) {
	switch {
	case cfg.WSL2 != nil:
		return newWSL2Backend(ctx, cfg, status)
	case cfg.WSLC != nil:
		return newWSLBackend(ctx, cfg, status)
	}
	return nil, errors.New("Windows 执行器需要选择 WSLC 或 WSL2 后端")
}

func newWSLBackend(ctx context.Context, cfg Config, status func(Status)) (backend, error) {
	if err := cfg.WSLC.Validate(); err != nil {
		return nil, err
	}
	path, err := wslc.Find()
	if err != nil {
		return nil, err
	}
	cli := wslc.Client{Path: path}
	version, err := cli.Version(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireSupportedVersion(version); err != nil {
		return nil, err
	}
	if removed, err := removeOrphans(ctx, cli, status); err != nil {
		return nil, err
	} else if removed > 0 {
		status(Status{State: "available", Message: fmt.Sprintf("已清理 %d 个上次遗留的执行容器", removed)})
	}
	return &wslcBackend{path: path, version: version}, nil
}

// requireSupportedVersion refuses a CLI older than the 2.9.3 that introduced
// containers. Unparseable output is accepted so a future naming change does not
// block a working installation.
func requireSupportedVersion(version string) error {
	fields := strings.Fields(version)
	for _, field := range fields {
		parts := strings.SplitN(strings.TrimPrefix(field, "v"), ".", 3)
		if len(parts) < 2 {
			continue
		}
		major, minor, ok := atoi(parts[0]), atoi(parts[1]), true
		if major < 0 || minor < 0 {
			ok = false
		}
		if !ok {
			continue
		}
		if major < 2 || major == 2 && minor < 9 {
			return fmt.Errorf("wslc 版本 %s 过旧，请更新到 WSL 2.9.3 或更高版本", field)
		}
		return nil
	}
	return nil
}

func atoi(value string) int {
	number := 0
	if value == "" {
		return -1
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return -1
		}
		number = number*10 + int(r-'0')
	}
	return number
}

// removeOrphans deletes containers a previous run left behind: a killed
// supervisor cannot stop its container, so startup reconciles ownership.
func removeOrphans(ctx context.Context, cli wslc.Client, status func(Status)) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	output, err := cli.Output(ctx, wslc.ListArgs()...)
	if err != nil {
		return 0, fmt.Errorf("无法列出 wslc 容器: %w", err)
	}
	names, err := wslc.ParseList(output)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, name := range names {
		if err := cli.Command(ctx, wslc.RemoveArgs(name)...).Run(); err != nil {
			status(Status{State: "reconnecting", Message: "无法清理遗留容器 " + name})
			continue
		}
		removed++
	}
	return removed, nil
}

func deviceLabels(cfg Config) []string {
	if cfg.WSL2 != nil {
		return []string{"linux-" + goruntime.GOARCH + "-wsl2"}
	}
	return []string{"linux-" + goruntime.GOARCH + "-wslc"}
}

// describe reports the container backend and the wslc version it drives.
func (b *wslcBackend) describe() descriptor {
	return descriptor{backend: "wslc", engine: b.version}
}

func (b *wslcBackend) executeLease(ctx context.Context, c *client, _ string, grant run.ExternalGrant, token string, status func(Status)) Status {
	result := Status{State: "failed", Job: &grant}
	config := *c.config.WSLC
	name := wslc.Name(grant.RunID, grant.JobID, run.ExternalAttempt(token))
	controlURL := c.url.String()
	if config.ControlURL != "" {
		controlURL = config.ControlURL
	}
	request := ExecRequest{URL: controlURL, JobID: grant.JobID, Attempt: token, Workspace: wslc.Workspace}
	if config.CAFile != "" {
		cert, err := os.ReadFile(config.CAFile)
		if err != nil {
			result.Message = "无法读取 wslc 证书文件"
			return result
		}
		request.CACert = string(cert)
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
	command := exec.Command(b.path, wslc.RunArgs(config, name, grant.JobID, token)...)
	command.Stdin = stdin
	command.Stdout = stdoutWriter
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		_ = stdinWriter.Close()
		_ = stdoutWriter.Close()
		result.Message = "无法启动 wslc，请检查 WSL 安装"
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
		// The pipe stays open so closing it is the stop signal.
		select {
		case <-ctx.Done():
		case <-finished:
		}
		closeStdin()
	}()
	go func() {
		// A cancelled lease stops the job the same way closing the window does:
		// the container's stdin ends, then the container is stopped if it does
		// not shut down in time. The CLI process is killed last so a hung
		// container cannot keep the runner alive.
		select {
		case <-ctx.Done():
		case <-finished:
			return
		}
		closeStdin()
		timer := time.NewTimer(stopGrace)
		defer timer.Stop()
		select {
		case <-finished:
			return
		case <-timer.C:
		}
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopWait)
		defer cancel()
		_ = wslc.Client{Path: b.path}.Command(stopCtx, wslc.StopArgs(name, 10)...).Run()
		select {
		case <-finished:
		case <-time.After(stopWait):
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
			state := update.State
			mu.Unlock()
			if jobTerminal(state) {
				status(Status{State: DesktopState(run.State(state)), Message: update.Message, Job: &grant})
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
			// The container reported an error of its own, such as a missing
			// credential or an unusable control-plane response.
			result.Message = "任务未成功：" + firstLine(detail)
		} else if detail := stderr.Meaningful(); detail != "" {
			result.Message = "任务未成功：" + detail
		} else if waitErr != nil {
			result.Message = fmt.Sprintf("wslc 退出：%v", waitErr)
		}
	}
	removeContainer(ctx, wslc.Client{Path: b.path}, name)
	return result
}

// jobTerminal reports whether a container's state line ends the attempt. The
// container emits control-plane states (success, cancelled, ...) or the desktop
// ones it reports for a stop, so both vocabularies count.
func jobTerminal(state string) bool {
	if run.State(state).Terminal() {
		return true
	}
	switch state {
	case "succeeded", "failed", "stopped", "timed_out":
		return true
	}
	return false
}

// removeContainer frees the attempt's disk. Its result is not reported: the job
// verdict already came from the control plane or the container exit status.
func removeContainer(ctx context.Context, cli wslc.Client, name string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	_ = cli.Command(ctx, wslc.RemoveArgs(name)...).Run()
}

type tailWriter struct {
	mu    sync.Mutex
	data  []byte
	limit int
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.data = append(t.data, p...)
	if len(t.data) > t.limit {
		t.data = t.data[len(t.data)-t.limit:]
	}
	return len(p), nil
}

func (t *tailWriter) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.data))
}

// Meaningful returns the first stderr line that is not the CLI's standing
// warning about the session's missing swap accounting, which it prints for
// every container on a host without cgroup swap support.
func (t *tailWriter) Meaningful() string {
	for line := range strings.SplitSeq(t.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "swap limit capabilities") {
			continue
		}
		if len(line) > 200 {
			line = line[:200]
		}
		return line
	}
	return ""
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	if len(line) > 200 {
		line = line[:200]
	}
	return strings.TrimSpace(line)
}
