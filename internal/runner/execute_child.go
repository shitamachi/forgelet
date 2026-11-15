package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/shitamachi/forgelet/internal/run"
)

// executeChild streams one leased attempt over stdin/stdout. EOF gives the
// worker time to flush its result before the supervisor forces shutdown.
func executeChild(ctx context.Context, command *exec.Cmd, request ExecRequest, grant run.ExternalGrant, status func(Status), stop func()) Status {
	result := Status{State: "failed", Job: &grant}
	if ctx.Err() != nil {
		result.State = "stopped"
		return result
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
	command.Stdin = stdin
	command.Stdout = stdoutWriter
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		_ = stdinWriter.Close()
		_ = stdoutWriter.Close()
		result.Message = "无法启动执行器：" + firstLine(err.Error())
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
		timer := time.NewTimer(20 * time.Second)
		defer timer.Stop()
		select {
		case <-finished:
			return
		case <-timer.C:
		}
		if stop != nil {
			stop()
		}
		// Closing stdin is the protocol's stop signal; killing the client is the
		// last resort when the distribution does not react in time.
		select {
		case <-finished:
		case <-time.After(25 * time.Second):
			_ = command.Process.Kill()
		}
	}()
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
			if reported != "" {
				continue
			}
			if jobTerminal(update.State) {
				reported, message = run.State(update.State), update.Message
				status(Status{State: DesktopState(reported), Message: message, Job: &grant})
			} else if update.Message != "" {
				message = update.Message
			}
		}
	}()
	waitErr := command.Wait()
	close(finished)
	closeStdin()
	<-readerDone
	state, detail := reported, message
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
			result.Message = fmt.Sprintf("执行器退出：%v", waitErr)
		}
	}
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
