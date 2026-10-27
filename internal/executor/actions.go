package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shitamachi/forgelet/internal/action"
	"github.com/shitamachi/forgelet/internal/expression"
	"github.com/shitamachi/forgelet/internal/run"
	"github.com/shitamachi/forgelet/internal/workflow"
)

var errActionTimeout = errors.New("action step timed out")

type actionSession struct {
	workspace, meta string
	opts            Options
	mask            *masker
	posts           []actionPost
	sources         map[string]string
	count           int
	cacheSaves      []func(context.Context) error
}
type actionPost struct {
	definition workflow.ActionDefinition
	directory  string
	plan       run.Plan
	env        map[string]string
	files      map[string]string
	stepID     string
}

func (s *actionSession) source(ctx context.Context, a run.Action) (string, error) {
	if a.Repository == "" {
		return withinWorkspace(s.workspace, a.Path, false)
	}
	key := a.Repository + "@" + a.SHA
	directory := s.sources[key]
	if directory == "" {
		var err error
		directory, err = os.MkdirTemp(s.meta, "action-")
		if err != nil {
			return "", err
		}
		client := s.opts.ActionClient
		if client == nil {
			client = &http.Client{Timeout: 2 * time.Minute}
		}
		if err := action.Download(ctx, client, s.opts.ActionBaseURL, a, directory); err != nil {
			return "", err
		}
		s.sources[key] = directory
	}
	return withinWorkspace(directory, a.Path, false)
}
func loadAction(directory string) (workflow.ActionDefinition, error) {
	filename, err := withinWorkspace(directory, "action.yml", false)
	if errors.Is(err, os.ErrNotExist) {
		filename, err = withinWorkspace(directory, "action.yaml", false)
	}
	if err != nil {
		return workflow.ActionDefinition{}, fmt.Errorf("action metadata not found: %w", err)
	}
	file, err := os.Open(filename)
	if err != nil {
		return workflow.ActionDefinition{}, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, (256<<10)+1))
	if err != nil {
		return workflow.ActionDefinition{}, err
	}
	return workflow.ParseAction(data)
}
func (s *actionSession) invoke(ctx context.Context, plan run.Plan, step run.Step, env, global map[string]string, files map[string]string, sink *logSink, outputs map[string]map[string]string, records map[string]run.StepResult, depth int, status expression.Context) (int, error) {
	if step.Action.Repository == "actions/setup-go" || step.Action.Repository == "actions/setup-node" {
		copied := *step.Action
		copied.Inputs = copyEnvironment(step.Action.Inputs)
		if _, present := copied.Inputs["token"]; !present {
			copied.Inputs["token"] = ""
		}
		step.Action = &copied
		if err := action.ValidateNative(step.Action); err != nil {
			return -1, err
		}
	}
	s.count++
	if depth > 8 || s.count > 256 {
		return -1, fmt.Errorf("action nesting or invocation limit exceeded")
	}
	if action.BuiltinInputs(step.Action.Uses) != nil {
		return s.builtin(ctx, plan, step, env, files, sink, outputs, records)
	}
	directory, err := s.source(ctx, *step.Action)
	if err != nil {
		return -1, err
	}
	definition, err := loadAction(directory)
	if err != nil {
		return -1, err
	}
	inputs := map[string]string{}
	for key, value := range definition.Inputs {
		if provided, ok := step.Action.Inputs[key]; ok {
			value = provided
		}
		expanded, err := interpolate(value, plan, env, outputs, records)
		if err != nil {
			return -1, fmt.Errorf("action input %s: %w", key, err)
		}
		inputs[key] = expanded
	}
	for key := range step.Action.Inputs {
		if _, ok := definition.Inputs[key]; !ok {
			return -1, fmt.Errorf("unsupported action input %q", key)
		}
	}
	plan.ActionInputs, plan.ActionPath = inputs, directory
	env = copyEnvironment(env)
	env["GITHUB_ACTION_PATH"] = directory
	if definition.Using == "composite" {
		return s.composite(ctx, plan, definition, env, global, files, sink, depth+1, status)
	}
	for key, value := range inputs {
		env["INPUT_"+strings.ToUpper(key)] = value
	}
	statePath := files["STATE"]
	if definition.Pre != "" {
		allowed, err := expression.Evaluate(definition.PreIf, conditionContext(plan, env, outputs, records, status.Success, status.Failure))
		if err != nil {
			return -1, err
		}
		if allowed {
			code, err := s.node(ctx, definition.Using, definition.Pre, directory, env, sink)
			if err != nil || code != 0 {
				return code, err
			}
			if _, err := applyFileCommands(files, global); err != nil {
				return -1, err
			}
			if _, err := applyFileCommands(files, env); err != nil {
				return -1, err
			}
			if err := loadState(statePath, env); err != nil {
				return -1, err
			}
			for _, key := range []string{"ENV", "PATH", "OUTPUT"} {
				if err := os.WriteFile(files[key], nil, 0600); err != nil {
					return -1, err
				}
			}
		}
	}
	if definition.Post != "" {
		s.posts = append(s.posts, actionPost{definition: definition, directory: directory, plan: plan, env: copyEnvironment(env), files: files, stepID: step.ID})
	}
	return s.node(ctx, definition.Using, definition.Main, directory, env, sink)
}
func loadState(file string, env map[string]string) error {
	raw, err := readCommandFile(file)
	if err != nil {
		return err
	}
	state, err := parseCommandPairs(raw)
	if err != nil {
		return err
	}
	for key, value := range state {
		env["STATE_"+key] = value
	}
	return nil
}
func (s *actionSession) node(ctx context.Context, using, script, directory string, env map[string]string, sink io.Writer) (int, error) {
	target, err := withinWorkspace(directory, script, false)
	if err != nil {
		return -1, err
	}
	binary := s.opts.NodeRuntimes[using]
	if binary == "" {
		binary = "/opt/forgelet/" + using + "/bin/node"
		if _, err := os.Stat(binary); err != nil {
			binary, err = exec.LookPath("node")
			if err != nil {
				return -1, fmt.Errorf("%s runtime is required", using)
			}
		}
	}
	version, err := exec.CommandContext(ctx, binary, "--version").Output()
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(version)), "v"+strings.TrimPrefix(using, "node")+".") {
		return -1, fmt.Errorf("configured %s runtime has a different major version", using)
	}
	command := exec.CommandContext(ctx, binary, target)
	command.Dir = s.workspace
	command.Env = environmentList(env)
	command.Stdout, command.Stderr = sink, sink
	return runProcess(ctx, command)
}
func (s *actionSession) composite(ctx context.Context, plan run.Plan, definition workflow.ActionDefinition, env, global, outerFiles map[string]string, sink *logSink, depth int, status expression.Context) (int, error) {
	outputs := map[string]map[string]string{}
	records := map[string]run.StepResult{}
	failed := false
	timedOutSeen := false
	for index, step := range definition.Steps {
		if err := ctx.Err(); err != nil {
			return -1, err
		}
		local := copyEnvironment(env)
		for key, value := range step.Env {
			v, err := interpolate(value, plan, env, outputs, records)
			if err != nil {
				return -1, err
			}
			local[key] = v
		}
		eval := conditionContext(plan, local, outputs, records, !failed, failed || plan.AncestorFailed)
		allowed, err := expression.Evaluate(step.If, eval)
		if err != nil {
			return -1, err
		}
		if !allowed {
			records[step.ID] = run.StepResult{ID: step.ID, State: run.Skipped, Outcome: run.Skipped}
			outputs[step.ID] = nil
			continue
		}
		directory, err := os.MkdirTemp(s.meta, "composite-")
		if err != nil {
			return -1, err
		}
		files, err := commandFiles(directory, "step", local)
		if err != nil {
			return -1, err
		}
		stepCtx, cancel := context.WithCancel(ctx)
		if step.TimeoutSeconds > 0 {
			cancel()
			stepCtx, cancel = context.WithTimeout(ctx, time.Duration(step.TimeoutSeconds)*time.Second)
		}
		var code int
		switch {
		case step.Action != nil:
			code, err = s.invoke(stepCtx, plan, step, local, global, files, sink, outputs, records, depth, expression.Context{Success: status.Success && !failed, Failure: status.Failure || failed})
		case step.Checkout != nil:
			code, err = checkout(stepCtx, plan, *step.Checkout, s.opts, s.workspace, local, sink)
		default:
			code, err = shellStep(stepCtx, plan, step, s.workspace, directory, index, local, outputs, records, sink)
		}
		timedOut := errors.Is(stepCtx.Err(), context.DeadlineExceeded) || errors.Is(err, errActionTimeout)
		if ctx.Err() != nil {
			cancel()
			return -1, ctx.Err()
		}
		cancel()
		if timedOut && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errActionTimeout)) {
			err = nil
		}
		if err != nil {
			return code, err
		}
		timedOutSeen = timedOutSeen || timedOut
		changes, err := applyFileCommands(files, global)
		if err != nil {
			return -1, err
		}
		if _, err := outputSize(changes); err != nil {
			return -1, err
		}
		if _, err := applyFileCommands(files, env); err != nil {
			return -1, err
		}
		state := run.Succeeded
		if code != 0 {
			state = run.Failed
		}
		if timedOut {
			state = run.TimedOut
		}
		record := run.StepResult{ID: step.ID, State: state, Outcome: state, ExitCode: code, Outputs: changes}
		ignore := step.ContinueOnError
		if step.ContinueOnErrorIf != "" {
			v, err := expression.Eval(step.ContinueOnErrorIf, eval)
			if err != nil {
				return -1, err
			}
			var ok bool
			ignore, ok = v.(bool)
			if !ok {
				return -1, fmt.Errorf("continue-on-error must return boolean")
			}
		}
		if state == run.Failed && ignore {
			record.State = run.Succeeded
		}
		if record.State != run.Succeeded {
			failed = true
		}
		records[step.ID] = record
		outputs[step.ID] = changes
	}
	values := map[string]string{}
	for key, value := range definition.Outputs {
		expanded, err := interpolate(value, plan, env, outputs, records)
		if err != nil {
			return -1, err
		}
		values[key] = expanded
	}
	if _, err := outputSize(values); err != nil {
		return -1, err
	}
	file, err := os.OpenFile(outerFiles["OUTPUT"], os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return -1, err
	}
	for _, key := range sortedMapKeys(values) {
		delimiter := "forgelet-output-" + strconv.Itoa(len(values[key]))
		for strings.Contains(values[key], delimiter) {
			delimiter += "x"
		}
		if _, err := fmt.Fprintf(file, "%s<<%s\n%s\n%s\n", key, delimiter, values[key], delimiter); err != nil {
			_ = file.Close()
			return -1, err
		}
	}
	if err := file.Close(); err != nil {
		return -1, err
	}
	if timedOutSeen {
		return -1, errActionTimeout
	}
	if failed {
		return 1, nil
	}
	return 0, nil
}
func commandFiles(directory, prefix string, env map[string]string) (map[string]string, error) {
	files := map[string]string{}
	for _, name := range []string{"ENV", "PATH", "OUTPUT", "STATE", "STEP_SUMMARY"} {
		file := filepath.Join(directory, prefix+"-"+name)
		if err := os.WriteFile(file, nil, 0600); err != nil {
			return nil, err
		}
		files[name] = file
		env["GITHUB_"+name] = file
	}
	return files, nil
}
func (s *actionSession) finish(ctx context.Context, global map[string]string, result *run.Result, returnErr *error) {
	defer func() {
		if ctx.Err() != nil || result.State != run.Succeeded || *returnErr != nil {
			return
		}
		for i := len(s.cacheSaves) - 1; i >= 0; i-- {
			if err := s.cacheSaves[i](ctx); err != nil {
				result.State = run.Failed
				*returnErr = err
				return
			}
		}
	}()
	if ctx.Err() != nil {
		return
	} // Explicit cancellation/job timeout stops all new code.
	for i := len(s.posts) - 1; i >= 0; i-- {
		post := s.posts[i]
		env := copyEnvironment(post.env)
		for key, value := range global {
			env[key] = value
		}

		if err := loadState(post.files["STATE"], env); err != nil {
			result.State = run.Failed
			*returnErr = err
			continue
		}
		allowed, err := expression.Evaluate(post.definition.PostIf, conditionContext(post.plan, env, nil, nil, result.State == run.Succeeded, result.State == run.Failed || result.State == run.TimedOut || result.State == run.Interrupted || post.plan.AncestorFailed))
		if err != nil {
			result.State = run.Failed
			*returnErr = err
			continue
		}
		if !allowed {
			continue
		}
		postCtx, cancel := context.WithCancel(ctx)
		emit := s.opts.Log
		if s.opts.ContextLog != nil {
			emit = func(id string, data []byte) error { return s.opts.ContextLog(postCtx, id, data) }
		}
		sink := &logSink{stepID: "post:" + post.stepID, mask: s.mask, emit: emit, cancel: cancel}
		code, err := s.node(postCtx, post.definition.Using, post.definition.Post, post.directory, env, sink)
		flushErr := sink.finish()
		cancel()
		if ctx.Err() != nil {
			result.State = contextState(ctx)
			return
		}
		if err != nil || flushErr != nil || code != 0 {
			result.State = run.Failed
			result.Error = "action post failed"
			if err != nil {
				*returnErr = err
			} else if flushErr != nil {
				*returnErr = flushErr
			}
		}
	}
}
