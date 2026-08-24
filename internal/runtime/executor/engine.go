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
	"github.com/shitamachi/forgelet/internal/workflow/expression"
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

	states := map[string]*stepState{}
	jobEnv := expression.NewEnv().With("github", githubOf(&p)).WithWorkspaceHasher(expression.NewDirHasher(e.WorkDir))

	// Job-level env values may carry expressions (github/env contexts only
	// at this point — steps do not exist yet).
	for k, v := range env {
		if !expression.HasExpression(v) {
			continue
		}
		rendered, ierr := expression.Interpolate(v, jobEnv)
		if ierr != nil {
			result.Error = "job env " + k + ": " + ierr.Error()
			e.report(ctx, id, result, logger)
			return result, fmt.Errorf("%w: %s", ErrStepFailed, result.Error)
		}
		env[k] = rendered
	}

	withSecrets := map[string]string{}
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
			if strings.HasPrefix(ref.Env, "$with:") {
				withSecrets[ref.Env] = val
				continue
			}
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

		exprEnv := e.exprEnv(&p, stepEnv, states)

		// Step-level condition (spec 0006 V1): a false `if:` records the
		// skipped outcome and the job keeps going.
		if step.If != "" {
			run, cerr := expression.EvaluateCondition(step.If, exprEnv)
			if cerr != nil {
				result.Error = "step " + step.ID + " if: " + cerr.Error()
				return result, e.failJob(ctx, id, p, i, &result, logger)
			}
			if !run {
				states[step.ID] = &stepState{outcome: outcomeSkipped, conclusion: conclusionSkip}
				result.Steps = append(result.Steps, StepResult{StepID: step.ID, Outcome: outcomeSkipped, Conclusion: conclusionSkip})
				continue
			}
		}

		if step.Builtin != nil {
			// Builtin step: resolve inputs (interpolate + overlay secrets).
			finalInputs := make(map[string]string, len(step.Builtin.Inputs))
			for k, v := range step.Builtin.Inputs {
				if expression.HasExpression(v) {
					rendered, ierr := expression.Interpolate(v, exprEnv)
					if ierr != nil {
						result.Error = "step " + step.ID + " with." + k + ": " + ierr.Error()
						return result, e.failJob(ctx, id, p, i, &result, logger)
					}
					finalInputs[k] = rendered
				} else {
					finalInputs[k] = v
				}
			}
			prefix := "$with:" + step.ID + ":"
			for envKey, secVal := range withSecrets {
				if strings.HasPrefix(envKey, prefix) {
					k := strings.TrimPrefix(envKey, prefix)
					finalInputs[k] = secVal
				}
			}
			// Interpolate any newly injected secret-derived values that may contain expressions (unlikely but safe)
			// Secret values are not interpolated.

			outputs := map[string]string{}
			bc := BuiltinContext{
				Ctx:       ctx,
				Workspace: e.WorkDir,
				Inputs:    finalInputs,
				Env:       stepEnv,
				Logger:    logger,
				SetOutput: func(k, v string) { outputs[k] = v },
			}
			handler, ok := builtinRegistry[step.Builtin.Action]
			if !ok {
				result.Error = "builtin " + step.Builtin.Action + " not found"
				return result, e.failJob(ctx, id, p, i, &result, logger)
			}
			start := time.Now()
			berr := handler(ctx, bc)
			sr := StepResult{StepID: step.ID, DurationMs: time.Since(start).Milliseconds()}
			state := &stepState{outputs: outputs}
			states[step.ID] = state
			if berr == nil {
				sr.ExitCode = 0
			} else {
				sr.ExitCode = 1
			}
			// Fold file commands (builtin may have written to GITHUB_ENV/OUTPUT via files)
			if kvs, ferr := filecommand.ParseKVFile(mustRead(envFile)); ferr == nil {
				_ = filecommand.Apply(env, kvs)
			} else {
				logger.Warn("malformed GITHUB_ENV", "step", step.ID, "err", ferr.Error())
			}
			if kvs, ferr := filecommand.ParseKVFile(mustRead(outFile)); ferr == nil {
				for _, kv := range kvs {
					state.outputs[kv.Key] = kv.Value
					logger.Info("step output", "step", step.ID, "key", kv.Key, "value", kv.Value)
				}
			} else {
				logger.Warn("malformed GITHUB_OUTPUT", "step", step.ID, "err", ferr.Error())
			}
			// Merge SetOutput outputs into state (already there) and also persist to file for consistency
			// Already in state.outputs.
			pathEntries = append(filecommand.ParsePathFile(mustRead(pathFile)), pathEntries...)

			switch {
			case berr == nil:
				sr.Outcome, sr.Conclusion = outcomeSuccess, conclusionOK
			case errors.Is(berr, context.Canceled) || errors.Is(berr, ErrCancelled):
				result.Cancelled = true
				result.Error = fmt.Sprintf("step %s cancelled", step.ID)
				e.report(ctx, id, result, logger)
				return result, ErrCancelled
			case step.ContinueOnError:
				sr.Outcome, sr.Conclusion = outcomeFailure, conclusionOK
				logger.Warn("step failed but continue-on-error is set", "step", step.ID, "err", berr.Error())
			default:
				sr.Outcome, sr.Conclusion = outcomeFailure, conclusionFail
				result.Steps = append(result.Steps, sr)
				result.Error = fmt.Sprintf("step %s failed: %v", step.ID, berr)
				return result, e.failJob(ctx, id, p, i+1, &result, logger)
			}
			state.outcome, state.conclusion = sr.Outcome, sr.Conclusion
			result.Steps = append(result.Steps, sr)
			continue
		}

		script := step.Run.Script
		if expression.HasExpression(script) {
			rendered, ierr := expression.Interpolate(script, exprEnv)
			if ierr != nil {
				result.Error = "step " + step.ID + ": " + ierr.Error()
				return result, e.failJob(ctx, id, p, i, &result, logger)
			}
			script = rendered
		}
		for k, v := range step.Run.Env {
			if !expression.HasExpression(v) {
				continue
			}
			rendered, ierr := expression.Interpolate(v, exprEnv)
			if ierr != nil {
				result.Error = "step " + step.ID + " env " + k + ": " + ierr.Error()
				return result, e.failJob(ctx, id, p, i, &result, logger)
			}
			stepEnv[k] = rendered
		}

		start := time.Now()
		exit, err := e.runStepScript(ctx, id, step, script, stepEnv, masker, logger)
		sr := StepResult{StepID: step.ID, ExitCode: exit, DurationMs: time.Since(start).Milliseconds()}
		state := &stepState{outputs: map[string]string{}}
		states[step.ID] = state

		// Fold file commands into the runtime context regardless of exit.
		if kvs, ferr := filecommand.ParseKVFile(mustRead(envFile)); ferr == nil {
			_ = filecommand.Apply(env, kvs)
		} else {
			logger.Warn("malformed GITHUB_ENV", "step", step.ID, "err", ferr.Error())
		}
		if kvs, ferr := filecommand.ParseKVFile(mustRead(outFile)); ferr == nil {
			for _, kv := range kvs {
				state.outputs[kv.Key] = kv.Value
				logger.Info("step output", "step", step.ID, "key", kv.Key, "value", kv.Value)
			}
		} else {
			logger.Warn("malformed GITHUB_OUTPUT", "step", step.ID, "err", ferr.Error())
		}
		pathEntries = append(filecommand.ParsePathFile(mustRead(pathFile)), pathEntries...)

		switch {
		case err == nil:
			sr.Outcome, sr.Conclusion = outcomeSuccess, conclusionOK
		case errors.Is(err, context.Canceled) || errors.Is(err, ErrCancelled):
			result.Cancelled = true
			result.Error = fmt.Sprintf("step %s cancelled", step.ID)
			e.report(ctx, id, result, logger)
			return result, ErrCancelled
		case step.ContinueOnError:
			// outcome failure, conclusion success: the job keeps going.
			sr.Outcome, sr.Conclusion = outcomeFailure, conclusionOK
			logger.Warn("step failed but continue-on-error is set",
				"step", step.ID, "exitCode", exit)
		default:
			sr.Outcome, sr.Conclusion = outcomeFailure, conclusionFail
			result.Steps = append(result.Steps, sr)
			result.Error = fmt.Sprintf("step %s exited with code %d", step.ID, exit)
			return result, e.failJob(ctx, id, p, i+1, &result, logger)
		}
		state.outcome, state.conclusion = sr.Outcome, sr.Conclusion
		result.Steps = append(result.Steps, sr)
	}

	result.Success = true
	e.report(ctx, id, result, logger)
	return result, nil
}

// failJob records the remaining steps as skipped into result and reports
// the failed job.
func (e *Engine) failJob(ctx context.Context, id identity.Identity, p plan.Plan, from int, result *JobResult, logger *slog.Logger) error {
	for _, step := range p.Steps[from:] {
		result.Steps = append(result.Steps, StepResult{StepID: step.ID, Outcome: outcomeSkipped, Conclusion: conclusionSkip})
	}
	e.report(ctx, id, *result, logger)
	return fmt.Errorf("%w: %s", ErrStepFailed, result.Error)
}

// report sends the terminal result; reporting problems are logged but never
// mask the execution outcome.
func (e *Engine) report(ctx context.Context, id identity.Identity, result JobResult, logger *slog.Logger) {
	if err := e.CP.ReportJob(ctx, id, result); err != nil {
		logger.Error("report job failed", "jobRun", id.JobRunID, "err", err.Error())
	}
}

// runStep executes one run step in its own process group, streaming output
// lines through the masker into structured logs. The script has already
// been expression-rendered by the caller.
func (e *Engine) runStepScript(ctx context.Context, id identity.Identity, step plan.Step, script string, env map[string]string, masker *mask.Masker, logger *slog.Logger) (int, error) {
	scriptPath := filepath.Join(e.WorkDir, ".forgelet", "step-"+step.ID+".sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
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
	cmd := exec.Command(shell, "-e", "-o", "pipefail", scriptPath)
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
