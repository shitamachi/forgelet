package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/shitamachi/forgelet/internal/run/plan"
	"github.com/shitamachi/forgelet/internal/runtime/executor/command"
	"github.com/shitamachi/forgelet/internal/runtime/executor/filecommand"
	"github.com/shitamachi/forgelet/internal/runtime/executor/mask"
	"github.com/shitamachi/forgelet/internal/security/identity"
)

// ErrStepFailed marks a non-zero step exit.
var ErrStepFailed = errors.New("executor: step failed")

// ErrCancelled marks a job terminated by cancellation or timeout.
var ErrCancelled = errors.New("executor: job cancelled")

// Engine executes the run steps of one plan in a shared workspace.
type Engine struct {
	CP         ControlPlane
	WorkDir    string
	Shell      string // default "bash"
	Grace      time.Duration
	Logger     *slog.Logger
	DefaultEnv map[string]string
}

// DefaultGrace is the SIGTERM→SIGKILL grace period for cancelled steps.
const DefaultGrace = 5 * time.Second

// Run fetches secrets, executes steps sequentially and reports the result.
// The returned error distinguishes step failure (ErrStepFailed), cancellation
// (ErrCancelled) and infrastructure errors.
func (e *Engine) Run(ctx context.Context, id identity.Identity, p plan.Plan) (JobResult, error) {
	if e.Shell == "" {
		e.Shell = "bash"
	}
	if e.Grace <= 0 {
		e.Grace = DefaultGrace
	}
	masker := mask.New()
	logger := slog.New(mask.NewHandler(e.Logger.Handler(), masker))

	result := JobResult{JobRunID: id.JobRunID}

	env := map[string]string{}
	for k, v := range e.DefaultEnv {
		env[k] = v
	}
	for k, v := range p.Env {
		env[k] = v
	}
	env["CI"] = "true"
	env["GITHUB_SHA"] = p.SHA
	env["GITHUB_REF"] = p.Ref
	env["GITHUB_JOB"] = p.JobRunID.CRName()

	if len(p.SecretRefs) > 0 {
		secrets, err := e.CP.FetchSecrets(ctx, id, p.SecretRefs)
		if err != nil {
			return result, fmt.Errorf("executor: fetch secrets: %w", err)
		}
		for _, ref := range p.SecretRefs {
			val, ok := secrets[ref.Scope+"/"+ref.Name]
			if !ok {
				continue
			}
			masker.Add(val)
			if ref.Env != "" {
				env[ref.Env] = val
			}
		}
	}

	path := filepath.Join(e.WorkDir, ".forgelet")
	if err := os.MkdirAll(path, 0o755); err != nil {
		return result, fmt.Errorf("executor: workspace metadata dir: %w", err)
	}

	var pathEntries []string
	for i, step := range p.Steps {
		select {
		case <-ctx.Done():
			result.Cancelled = true
			result.Error = "cancelled before step " + step.ID
			e.report(ctx, id, result, logger)
			return result, ErrCancelled
		default:
		}

		envFile := filepath.Join(path, fmt.Sprintf("step-%d.env", i))
		outFile := filepath.Join(path, fmt.Sprintf("step-%d.output", i))
		pathFile := filepath.Join(path, fmt.Sprintf("step-%d.path", i))
		for _, f := range []string{envFile, outFile, pathFile} {
			if err := os.WriteFile(f, nil, 0o644); err != nil {
				return result, fmt.Errorf("executor: prepare %s: %w", f, err)
			}
		}
		env["GITHUB_ENV"] = envFile
		env["GITHUB_OUTPUT"] = outFile
		env["GITHUB_PATH"] = pathFile

		stepEnv := map[string]string{}
		for k, v := range env {
			stepEnv[k] = v
		}
		for k, v := range step.Run.Env {
			stepEnv[k] = v
		}
		if len(pathEntries) > 0 {
			stepEnv["PATH"] = strings.Join(pathEntries, string(os.PathListSeparator)) + string(os.PathListSeparator) + stepEnv["PATH"]
		}

		start := time.Now()
		exit, err := e.runStep(ctx, id, step, stepEnv, masker, logger)
		sr := StepResult{StepID: step.ID, ExitCode: exit, DurationMs: time.Since(start).Milliseconds()}
		result.Steps = append(result.Steps, sr)

		// Fold file commands into the runtime context regardless of exit.
		if kvs, ferr := filecommand.ParseKVFile(mustRead(envFile)); ferr == nil {
			_ = filecommand.Apply(env, kvs)
		} else {
			logger.Warn("malformed GITHUB_ENV", "step", step.ID, "err", ferr.Error())
		}
		if kvs, ferr := filecommand.ParseKVFile(mustRead(outFile)); ferr == nil {
			for _, kv := range kvs {
				logger.Info("step output", "step", step.ID, "key", kv.Key, "value", kv.Value)
			}
		} else {
			logger.Warn("malformed GITHUB_OUTPUT", "step", step.ID, "err", ferr.Error())
		}
		pathEntries = append(filecommand.ParsePathFile(mustRead(pathFile)), pathEntries...)

		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, ErrCancelled) {
				result.Cancelled = true
				result.Error = fmt.Sprintf("step %s cancelled", step.ID)
				e.report(ctx, id, result, logger)
				return result, ErrCancelled
			}
			result.Error = fmt.Sprintf("step %s exited with code %d", step.ID, exit)
			e.report(ctx, id, result, logger)
			return result, fmt.Errorf("%w: %s", ErrStepFailed, result.Error)
		}
	}

	result.Success = true
	e.report(ctx, id, result, logger)
	return result, nil
}

// report sends the terminal result; reporting problems are logged but never
// mask the execution outcome.
func (e *Engine) report(ctx context.Context, id identity.Identity, result JobResult, logger *slog.Logger) {
	if err := e.CP.ReportJob(ctx, id, result); err != nil {
		logger.Error("report job failed", "jobRun", id.JobRunID, "err", err.Error())
	}
}

// runStep executes one run step in its own process group, streaming output
// lines through the masker into structured logs.
func (e *Engine) runStep(ctx context.Context, id identity.Identity, step plan.Step, env map[string]string, masker *mask.Masker, logger *slog.Logger) (int, error) {
	script := filepath.Join(e.WorkDir, ".forgelet", "step-"+step.ID+".sh")
	if err := os.WriteFile(script, []byte(step.Run.Script), 0o755); err != nil {
		return 0, fmt.Errorf("executor: write script: %w", err)
	}
	workDir := e.WorkDir
	if step.WorkingDir != "" {
		workDir = filepath.Join(e.WorkDir, step.WorkingDir)
	}

	shell := e.Shell
	if step.Run.Shell != "" {
		shell = step.Run.Shell
	}
	//nolint:noctx // cancellation is handled below via the process group
	// (SIGTERM/SIGKILL to -pgid); exec.CommandContext would only kill the
	// direct child and orphan step subprocesses.
	cmd := exec.Command(shell, "-e", "-o", "pipefail", script)
	cmd.Dir = workDir
	cmd.Env = envSlice(env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, fmt.Errorf("executor: stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout // merge; stream field still distinguishes nothing in M0
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("executor: start step %s: %w", step.ID, err)
	}

	stepLogger := logger.With("jobRun", string(id.JobRunID), "step", step.ID)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 0, 4096)
		chunk := make([]byte, 4096)
		for {
			n, rerr := stdout.Read(chunk)
			buf = append(buf, chunk[:n]...)
			for {
				idx := bytes.IndexByte(buf, '\n')
				if idx < 0 {
					break
				}
				line := strings.TrimSuffix(string(buf[:idx]), "\r")
				buf = buf[idx+1:]
				e.emitLine(stepLogger, masker, line)
			}
			if rerr != nil {
				if len(buf) > 0 {
					e.emitLine(stepLogger, masker, string(buf))
				}
				return
			}
		}
	}()

	// Cancellation: SIGTERM the process group, then SIGKILL after grace.
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	var werr error
	select {
	case werr = <-waitErr:
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case werr = <-waitErr:
		case <-time.After(e.Grace):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			werr = <-waitErr
		}
	}
	<-done

	if ctx.Err() != nil {
		return -1, ErrCancelled
	}
	if werr != nil {
		var exitErr *exec.ExitError
		if errors.As(werr, &exitErr) {
			return exitErr.ExitCode(), ErrStepFailed
		}
		return -1, fmt.Errorf("executor: wait step %s: %w", step.ID, werr)
	}
	return 0, nil
}

// emitLine handles workflow commands (add-mask takes immediate effect) and
// logs the (masked) line.
func (e *Engine) emitLine(logger *slog.Logger, masker *mask.Masker, line string) {
	if cmd, ok := command.Parse(line); ok {
		switch cmd.Name {
		case command.AddMask:
			masker.Add(cmd.Message)
		case command.Group, command.EndGroup:
			logger.Info("group", "action", cmd.Name, "title", cmd.Message)
		case command.Warning, command.Error, command.Notice, command.Debug:
			logger.Info("annotation", "level", cmd.Name, "message", cmd.Message, "params", fmt.Sprint(cmd.Parameters))
		}
		return
	}
	logger.Info("out", "message", line)
}

func envSlice(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

func mustRead(path string) []byte {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return b
}
