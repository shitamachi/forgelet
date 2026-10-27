// Package executor runs one immutable job plan without Kubernetes or database access.
package executor

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/shitamachi/forgelet/internal/expression"
	"github.com/shitamachi/forgelet/internal/run"
)

// Options supplies the execution workspace and scoped credentials. CloneToken is
// passed only to git child processes and is never persisted in a git config file.
type Options struct {
	ActCacheObjectBytes int64
	BuildkitConfig      string
	PackageToken        string
	ActCacheHost        string
	ImageMirrors        map[string]string
	ActImage            string
	ActWorker           string
	Services            Services
	Buildctl            string
	BuildAddress        string
	BuildImages         []string
	BuildInsecure       bool
	RegistryConfig      string
	Workspace           string
	Log                 func(stepID string, data []byte) error
	ContextLog          func(context.Context, string, []byte) error
	CloneURL            string
	CloneToken          string
	Masks               []string
	Secrets             map[string]string
	NodeRuntimes        map[string]string
	ActionClient        *http.Client
	ActionBaseURL       string
}

var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var outputKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
var commitSHA = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)

// Execute returns command failures as a failed Result with a nil error. An error
// indicates an executor, protocol, or logging failure and still carries a terminal Result.
func Execute(ctx context.Context, plan run.Plan, opts Options) (result run.Result, returnErr error) {
	if plan.Kernel == "act" {
		return executeAct(ctx, plan, opts)
	}
	if plan.Kernel != "" && plan.Kernel != "native" {
		return run.Result{State: run.Failed, Error: "unsupported job kernel"}, fmt.Errorf("unsupported job kernel %q", plan.Kernel)
	}
	result.State = run.Failed
	mask := newMasker()
	// Every executor/protocol failure must carry a non-empty public error.
	// storage.Finish uses an empty Result.Error to distinguish an ordinary
	// command failure (which job continue-on-error may tolerate) from an
	// infrastructure failure (which it must never hide).
	defer func() {
		if returnErr == nil {
			return
		}
		if result.State == run.Succeeded {
			result.State = run.Failed
		}
		if result.State == run.Failed && result.Error == "" {
			result.Error = publicError(returnErr, mask)
		}
	}()
	if err := prepareProcessReaping(); err != nil {
		return result, fmt.Errorf("prepare child process cleanup: %w", err)
	}
	if opts.Workspace == "" {
		return result, fmt.Errorf("executor workspace is required")
	}
	workspace, err := filepath.Abs(opts.Workspace)
	if err != nil {
		return result, fmt.Errorf("resolve workspace: %w", err)
	}
	if err := os.MkdirAll(workspace, 0700); err != nil {
		return result, fmt.Errorf("create workspace: %w", err)
	}
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		return result, fmt.Errorf("resolve workspace: %w", err)
	}
	meta, err := os.MkdirTemp("", "forgelet-job-")
	if err != nil {
		return result, fmt.Errorf("create execution metadata: %w", err)
	}
	defer func() { _ = os.RemoveAll(meta) }()
	meta, err = filepath.EvalSymlinks(meta)
	if err != nil {
		return result, fmt.Errorf("resolve execution metadata: %w", err)
	}
	eventPath := filepath.Join(meta, "event.json")
	payload := plan.Event
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	if err := os.WriteFile(eventPath, payload, 0600); err != nil {
		return result, fmt.Errorf("write event context: %w", err)
	}
	seconds := plan.TimeoutSeconds
	if seconds <= 0 {
		seconds = 1200
	}
	jobCtx, jobCancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
	defer jobCancel()
	plan.RuntimeSecrets = opts.Secrets
	if err := maskRegistry(opts.RegistryConfig, mask); err != nil {
		return result, err
	}
	for _, value := range opts.Secrets {
		if err := mask.add(value); err != nil {
			return result, err
		}
	}
	for _, value := range append(append([]string(nil), opts.Masks...), opts.CloneToken) {
		if err := mask.add(value); err != nil {
			return result, err
		}
	}
	if opts.CloneToken != "" {
		if err := mask.add(base64.StdEncoding.EncodeToString([]byte("x-access-token:" + opts.CloneToken))); err != nil {
			return result, err
		}
	}
	env := inheritedEnvironment()
	env["GITHUB_WORKSPACE"] = workspace
	env["GITHUB_EVENT_PATH"] = eventPath
	env["GITHUB_SHA"] = plan.SHA
	env["GITHUB_REF"] = plan.Ref
	public, _ := plan.Contexts()
	env["GITHUB_REF_TYPE"], env["GITHUB_REF_NAME"] = public["github.ref_type"], public["github.ref_name"]
	env["GITHUB_REPOSITORY"] = plan.Repository
	env["GITHUB_EVENT_NAME"] = plan.EventName
	if env["GITHUB_EVENT_NAME"] == "" {
		env["GITHUB_EVENT_NAME"] = "push"
	}
	env["CI"], env["GITHUB_ACTIONS"] = "true", "true"
	env["GITHUB_SERVER_URL"], env["GITHUB_API_URL"] = "https://github.com", "https://api.github.com"
	if plan.GitHubAPIURL != "" {
		env["GITHUB_API_URL"] = plan.GitHubAPIURL
	}
	for key, sub := range map[string]string{"RUNNER_TEMP": "temp", "RUNNER_TOOL_CACHE": "toolcache", "HOME": "home"} {
		env[key] = filepath.Join(meta, sub)
		if err := os.MkdirAll(env[key], 0700); err != nil {
			return result, err
		}
	}
	env["RUNNER_OS"] = "Linux"
	if runtime.GOOS == "darwin" {
		env["RUNNER_OS"] = "macOS"
	}
	env["RUNNER_ARCH"] = "X64"
	if runtime.GOARCH == "arm64" {
		env["RUNNER_ARCH"] = "ARM64"
	}
	outputs := make(map[string]map[string]string)
	stepResults := make(map[string]run.StepResult)
	// Evaluate declared job outputs at completion and redact the public result
	// once more, since a later step may register a mask for an earlier output.
	defer func() {
		outputErr := finalizeOutputs(plan, env, outputs, stepResults, mask, &result)
		if outputErr != nil && returnErr == nil {
			if result.State != run.Cancelled && result.State != run.TimedOut {
				result.State = run.Failed
			}
			result.Error = "executor could not publish job outputs"
			returnErr = outputErr
		}
	}()
	jobEnv := copyEnvironment(env)
	for _, key := range sortedMapKeys(plan.Env) {
		if err := validateConfiguredEnv(key, plan.Env[key]); err != nil {
			return result, err
		}
		value, err := interpolate(plan.Env[key], plan, env, outputs, stepResults)
		if err != nil {
			return result, fmt.Errorf("job environment: %w", err)
		}
		jobEnv[key] = value
	}
	env = jobEnv
	actions := &actionSession{workspace: workspace, meta: meta, opts: opts, mask: mask, sources: map[string]string{}}
	defer func() { actions.finish(jobCtx, env, &result, &returnErr) }()
	result.State = run.Succeeded
	failed := false
	outputBytes := 0
	for index, step := range plan.Steps {
		if jobCtx.Err() != nil {
			result.State = contextState(jobCtx)
			return result, nil
		}
		// Step conditions see this step's environment overrides. Evaluate each
		// override against the outer environment, then keep the merged map local.
		stepEnv := copyEnvironment(env)
		for _, key := range sortedMapKeys(step.Env) {
			if err := validateConfiguredEnv(key, step.Env[key]); err != nil {
				result.State = run.Failed
				return result, err
			}
			value, err := interpolate(step.Env[key], plan, env, outputs, stepResults)
			if err != nil {
				result.State = run.Failed
				return result, fmt.Errorf("step environment: %w", err)
			}
			stepEnv[key] = value
		}
		conditionCtx := conditionContext(plan, stepEnv, outputs, stepResults, !failed, failed || plan.AncestorFailed)
		shouldRun, err := expression.Evaluate(step.If, conditionCtx)
		if err != nil {
			result.State = run.Failed
			result.Steps = append(result.Steps, run.StepResult{ID: step.ID, ExitCode: -1, State: run.Failed, Outcome: run.Failed})
			return result, fmt.Errorf("evaluate step condition: %w", err)
		}
		if !shouldRun {
			skipped := run.StepResult{ID: step.ID, ExitCode: 0, State: run.Skipped, Outcome: run.Skipped}
			result.Steps = append(result.Steps, skipped)
			stepResults[step.ID] = skipped
			outputs[step.ID] = nil
			continue
		}
		stepResult := run.StepResult{ID: step.ID, State: run.Failed, Outcome: run.Failed, ExitCode: -1}
		// Errors preparing a step are infrastructure failures, not command exit
		// failures; continue-on-error must never suppress them.
		files, err := commandFiles(meta, strconv.Itoa(index), stepEnv)
		if err != nil {
			result.State = run.Failed
			return result, err
		}

		stepCtx, stepCancel := context.WithCancel(jobCtx)
		if step.TimeoutSeconds > 0 {
			stepCancel()
			stepCtx, stepCancel = context.WithTimeout(jobCtx, time.Duration(step.TimeoutSeconds)*time.Second)
		}
		emit := opts.Log
		if opts.ContextLog != nil {
			emit = func(id string, data []byte) error { return opts.ContextLog(stepCtx, id, data) }
		}
		sink := &logSink{stepID: step.ID, mask: mask, emit: emit, cancel: stepCancel}
		var exitCode int
		if step.Action != nil {
			exitCode, err = actions.invoke(stepCtx, plan, step, stepEnv, env, files, sink, outputs, stepResults, 0, conditionCtx)
		} else if step.Checkout != nil {
			exitCode, err = checkout(stepCtx, plan, *step.Checkout, opts, workspace, stepEnv, sink)
		} else {
			exitCode, err = shellStep(stepCtx, plan, step, workspace, meta, index, stepEnv, outputs, stepResults, sink)
		}
		finishErr := sink.finish()
		actionTimedOut := errors.Is(err, errActionTimeout)
		if actionTimedOut {
			err = nil
		}
		state := run.Succeeded
		if stepCtx.Err() != nil {
			state = contextState(stepCtx)
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				err = nil
			}
		} else if actionTimedOut {
			state = run.TimedOut
		} else if exitCode != 0 || err != nil || finishErr != nil {
			state = run.Failed
		}
		stepCancel()
		if finishErr != nil {
			if (state == run.Cancelled || state == run.TimedOut) && (errors.Is(finishErr, context.Canceled) || errors.Is(finishErr, context.DeadlineExceeded)) {
				err = nil
			} else {
				err = fmt.Errorf("upload step log: %w", finishErr)
				state = run.Failed
			}
		}
		if err == nil {
			stepResult.Outputs, err = applyFileCommands(files, env)
			if err == nil {
				var size int
				size, err = outputSize(stepResult.Outputs)
				outputBytes += size
				if outputBytes > maxOutputBytes {
					err = fmt.Errorf("combined step outputs exceed size limit")
				}
			}
			if err != nil {
				state = run.Failed
				stepResult.Outputs = nil
			}
		}
		stepResult.ExitCode = exitCode
		stepResult.Outcome = state
		stepResult.State = state
		allowFailure := step.ContinueOnError
		if step.ContinueOnErrorIf != "" {
			v, e := expression.Eval(step.ContinueOnErrorIf, conditionCtx)
			if e != nil {
				result.State = run.Failed
				return result, e
			}
			var ok bool
			allowFailure, ok = v.(bool)
			if !ok {
				result.State = run.Failed
				return result, fmt.Errorf("continue-on-error must evaluate to boolean")
			}
		}
		if state == run.Failed && err == nil && exitCode > 0 && allowFailure {
			stepResult.State = run.Succeeded
		}
		result.Steps = append(result.Steps, stepResult)
		stepResults[step.ID] = stepResult
		outputs[step.ID] = stepResult.Outputs
		if err != nil {
			result.State = run.Failed
			result.Error = "step " + step.ID + ": " + publicError(err, mask)
			return result, err
		}
		if jobCtx.Err() != nil || state == run.Cancelled {
			result.State = state
			if jobCtx.Err() != nil {
				result.State = contextState(jobCtx)
			}
			return result, nil
		}
		if stepResult.State != run.Succeeded {
			failed = true
			if state == run.TimedOut || result.State != run.TimedOut {
				result.State = state
			}
		}
	}
	return result, nil
}

func shellStep(ctx context.Context, plan run.Plan, step run.Step, workspace, meta string, index int, env map[string]string, outputs map[string]map[string]string, stepResults map[string]run.StepResult, sink io.Writer) (int, error) {
	script, err := interpolate(step.Run, plan, env, outputs, stepResults)
	if err != nil {
		return -1, err
	}
	if strings.TrimSpace(script) == "" {
		return -1, fmt.Errorf("run script is empty")
	}
	directory, err := interpolate(step.WorkingDirectory, plan, env, outputs, stepResults)
	if err != nil {
		return -1, err
	}
	directory, err = withinWorkspace(workspace, directory, false)
	if err != nil {
		return -1, err
	}
	scriptFile := filepath.Join(meta, fmt.Sprintf("%d.sh", index))
	if err := os.WriteFile(scriptFile, []byte(script), 0600); err != nil {
		return -1, fmt.Errorf("write step script: %w", err)
	}
	var cmd *exec.Cmd
	switch step.Shell {
	case "", "bash":
		cmd = exec.CommandContext(ctx, "bash", "--noprofile", "--norc", "-e", "-o", "pipefail", scriptFile)
	case "sh":
		cmd = exec.CommandContext(ctx, "sh", "-e", scriptFile)
	default:
		return -1, fmt.Errorf("unsupported shell")
	}
	cmd.Dir = directory
	cmd.Env = environmentList(env)
	cmd.Stdout, cmd.Stderr = sink, sink
	return runProcess(ctx, cmd)
}

func runProcess(ctx context.Context, cmd *exec.Cmd) (int, error) {
	if ctx.Err() != nil {
		return -1, ctx.Err()
	}
	// Own the output pipe rather than letting exec.Cmd copy to the log sink.
	// Cmd.WaitDelay also limits that copy, so a healthy but slow HTTP upload
	// would otherwise be mistaken for an escaped child holding stdout open.
	reader, writer, err := os.Pipe()
	if err != nil {
		return -1, fmt.Errorf("create step output pipe: %w", err)
	}
	defer func() { _ = reader.Close() }()
	sink := cmd.Stdout
	if sink == nil {
		sink = io.Discard
	}
	cmd.Stdout, cmd.Stderr = writer, writer
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 0
	if err := cmd.Start(); err != nil {
		_ = writer.Close()
		return -1, fmt.Errorf("start step process: %w", err)
	}
	_ = writer.Close()
	outputDone := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(sink, reader)
		outputDone <- copyErr
	}()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
		// Background children do not outlive their step.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case err = <-done:
			if !timer.Stop() {
				<-timer.C
			}
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		case <-timer.C:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			err = <-done
		}
	}
	reapProcessGroup(cmd.Process.Pid)
	var outputErr error
	select {
	case outputErr = <-outputDone:
	case <-ctx.Done():
		// Closing the read end also releases a reader if a detached descendant
		// escaped the process group and retained its inherited stdout handle.
		_ = reader.Close()
		outputErr = <-outputDone
	}
	if ctx.Err() != nil {
		return -1, ctx.Err()
	}
	if outputErr != nil {
		return -1, fmt.Errorf("read step output: %w", outputErr)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	if err != nil {
		return -1, fmt.Errorf("wait for step process: %w", err)
	}
	return 0, nil
}

func checkout(ctx context.Context, plan run.Plan, checkout run.Checkout, opts Options, workspace string, env map[string]string, sink io.Writer) (int, error) {
	if !commitSHA.MatchString(plan.SHA) || checkout.FetchDepth < 0 || opts.CloneURL == "" {
		return -1, fmt.Errorf("checkout requires a fixed commit SHA, clone URL, and nonnegative depth")
	}
	if strings.ContainsAny(opts.CloneURL, "\r\n\x00") || strings.HasPrefix(opts.CloneURL, "-") {
		return -1, fmt.Errorf("invalid clone URL")
	}
	parsed, err := url.Parse(opts.CloneURL)
	if err != nil || parsed.User != nil || (parsed.Scheme != "" && parsed.Scheme != "https" && parsed.Scheme != "http" && parsed.Scheme != "file") {
		return -1, fmt.Errorf("clone URL must not include credentials and must use HTTPS or a local repository")
	}
	if opts.CloneToken != "" && parsed.Scheme != "https" {
		return -1, fmt.Errorf("clone credentials require HTTPS")
	}
	directory, err := withinWorkspace(workspace, checkout.Path, true)
	if err != nil {
		return -1, err
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return -1, fmt.Errorf("create checkout directory: %w", err)
	}
	gitEnv := copyEnvironment(env)
	gitEnv["GIT_TERMINAL_PROMPT"] = "0"
	gitEnv["GIT_CONFIG_NOSYSTEM"] = "1"
	gitEnv["GIT_CONFIG_GLOBAL"] = "/dev/null"
	gitEnv["GIT_CONFIG_COUNT"] = "0"
	if opts.CloneToken != "" {
		gitEnv["GIT_CONFIG_COUNT"] = "1"
		gitEnv["GIT_CONFIG_KEY_0"] = "http.extraHeader"
		gitEnv["GIT_CONFIG_VALUE_0"] = "AUTHORIZATION: basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+opts.CloneToken))
	}
	commands := [][]string{{"init", "--quiet"}, {"remote", "add", "origin", opts.CloneURL}}
	if _, err := os.Stat(filepath.Join(directory, ".git")); err == nil {
		// The builtin supports a single checkout per destination, avoiding local
		// repository configuration inherited from earlier untrusted steps.
		return -1, fmt.Errorf("checkout destination already contains a Git repository")
	}
	if checkout.FetchDepth == 0 {
		commands = append(commands, []string{"fetch", "--no-recurse-submodules", "origin", "+refs/heads/*:refs/remotes/origin/*", "+refs/tags/*:refs/tags/*"})
	}
	fetch := []string{"fetch", "--no-recurse-submodules", "--no-tags"}
	if checkout.FetchDepth > 0 {
		fetch = append(fetch, "--depth="+strconv.Itoa(checkout.FetchDepth))
	}
	commands = append(commands, append(fetch, "origin", plan.SHA), []string{"checkout", "--force", "--detach", plan.SHA})
	for _, args := range commands {
		args = append([]string{"-c", "credential.helper=", "-c", "core.hooksPath=/dev/null", "-c", "http.followRedirects=false"}, args...)
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = directory
		cmd.Env = environmentList(gitEnv)
		cmd.Stdout, cmd.Stderr = sink, sink
		code, err := runProcess(ctx, cmd)
		if code != 0 || err != nil || ctx.Err() != nil {
			return code, err
		}
	}
	return 0, nil
}

func interpolate(value string, plan run.Plan, env map[string]string, outputs map[string]map[string]string, records ...map[string]run.StepResult) (string, error) {
	var stepResults map[string]run.StepResult
	if len(records) > 0 {
		stepResults = records[0]
	}
	ctx := conditionContext(plan, env, outputs, stepResults, true, false)
	allowed := func(ref string) bool {
		parts := strings.Split(ref, ".")
		if parts[0] == "steps" {
			if len(parts) == 1 || parts[1] == "*" {
				return true
			}
			_, ok := outputs[parts[1]]
			return ok
		}
		if parts[0] == "needs" {
			if len(parts) == 1 || parts[1] == "*" {
				return true
			}
			_, ok := directDependency(plan, parts[1])
			return ok
		}
		switch parts[0] {
		case "github":
			if ref == "github" || ref == "github.event" || strings.HasPrefix(ref, "github.event.") {
				return true
			}
			_, ok := ctx.Values[ref]
			return ok
		case "secrets":
			return slices.Contains(plan.SecretNames, strings.TrimPrefix(ref, "secrets."))
		case "env", "matrix", "strategy", "inputs", "vars", "runner":
			return true
		}
		return false
	}
	if err := expression.ValidateTemplate(value, allowed); err != nil {
		return "", err
	}
	return expression.Interpolate(value, ctx)
}

func withinWorkspace(workspace, relative string, missing bool) (string, error) {
	if filepath.IsAbs(relative) || strings.ContainsAny(relative, "\\\x00") {
		return "", fmt.Errorf("path must be relative to the workspace")
	}
	dest := filepath.Join(workspace, relative)
	if !isWithin(workspace, dest) {
		return "", fmt.Errorf("path escapes the workspace")
	}
	probe := dest
	for {
		resolved, err := filepath.EvalSymlinks(probe)
		if err == nil {
			if !isWithin(workspace, resolved) {
				return "", fmt.Errorf("symlink escapes the workspace")
			}
			return dest, nil
		}
		if !missing || !errors.Is(err, os.ErrNotExist) || probe == workspace {
			return "", fmt.Errorf("resolve working directory: %w", err)
		}
		probe = filepath.Dir(probe)
	}
}

func isWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func contextState(ctx context.Context) run.State {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return run.TimedOut
	}
	return run.Cancelled
}

func validateEnvKey(key string) error {
	if !envKey.MatchString(key) || strings.HasPrefix(key, "GITHUB_") || strings.HasPrefix(key, "RUNNER_") || strings.HasPrefix(key, "FORGELET_") || key == "NODE_OPTIONS" {
		return fmt.Errorf("invalid or reserved environment variable")
	}
	return nil
}

func inheritedEnvironment() map[string]string {
	env := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok && key != "TOKEN_FILE" && key != "POD_UID" && key != "JOB_ID" && key != "WORKSPACE" && !strings.HasPrefix(key, "FORGELET_") && validateEnvKey(key) == nil && !strings.HasPrefix(key, "GIT_CONFIG_") && key != "GIT_DIR" && key != "GIT_WORK_TREE" {
			env[key] = value
		}
	}
	return env
}

func copyEnvironment(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func environmentList(env map[string]string) []string {
	list := make([]string, 0, len(env))
	for _, key := range sortedMapKeys(env) {
		list = append(list, key+"="+env[key])
	}
	return list
}

func sortedMapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func publicError(err error, mask *masker) string {
	message := err.Error()
	for _, secret := range mask.values {
		message = strings.ReplaceAll(message, secret, "***")
	}
	if len(message) > 8192 {
		message = message[:8192] + "..."
	}
	return message
}

func validateConfiguredEnv(key, value string) error {
	if run.TokenEnvironment(key, value) {
		return nil
	}
	return validateEnvKey(key)
}
