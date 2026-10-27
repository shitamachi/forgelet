package executor

import (
	"fmt"
	"strings"

	"github.com/shitamachi/forgelet/internal/expression"
	"github.com/shitamachi/forgelet/internal/run"
)

const (
	maxOutputBytes = 64 << 10
	maxOutputCount = 100
)

func conditionContext(plan run.Plan, env map[string]string, outputs map[string]map[string]string, steps map[string]run.StepResult, success, failure bool) expression.Context {
	values, typed := plan.Contexts()
	for key, name := range map[string]string{"os": "RUNNER_OS", "arch": "RUNNER_ARCH", "temp": "RUNNER_TEMP", "tool_cache": "RUNNER_TOOL_CACHE"} {
		values["runner."+key] = env[name]
	}
	for key, value := range env {
		values["env."+key] = value
	}
	for id, step := range steps {
		values["steps."+id+".outcome"] = contextResult(step.Outcome)
		values["steps."+id+".conclusion"] = contextResult(step.State)
		for key, value := range outputs[id] {
			values["steps."+id+".outputs."+key] = value
		}
	}
	for _, id := range plan.Needs {
		dependency, ok := plan.NeedsContext[id]
		if !ok {
			continue
		}
		local := id
		if alias, ok := plan.NeedsAliases[id]; ok {
			local = alias
		}
		if id == plan.ParentGate {
			continue
		}
		values["needs."+local+".result"] = contextResult(dependency.Result)
		for key, value := range dependency.Outputs {
			values["needs."+local+".outputs."+key] = value
		}
	}
	return expression.Context{Values: values, Typed: typed, Success: success, Failure: failure}
}

func contextResult(state run.State) string {
	if state == run.TimedOut || state == run.Interrupted {
		return string(run.Failed)
	}
	return string(state)
}

func directDependency(plan run.Plan, id string) (run.Dependency, bool) {
	for _, need := range plan.Needs {
		if need == plan.ParentGate {
			continue
		}
		if need == id || plan.NeedsAliases[need] == id {
			dependency, ok := plan.NeedsContext[need]
			return dependency, ok
		}
	}
	return run.Dependency{}, false
}

func outputSize(outputs map[string]string) (int, error) {
	if len(outputs) > maxOutputCount {
		return 0, fmt.Errorf("output count exceeds limit")
	}
	size := 0
	for key, value := range outputs {
		if !outputKey.MatchString(key) || strings.ContainsRune(value, 0) {
			return 0, fmt.Errorf("invalid output key or value")
		}
		size += len(key) + len(value)
		if size > maxOutputBytes {
			return 0, fmt.Errorf("outputs exceed size limit")
		}
	}
	return size, nil
}

func publicOutputs(outputs map[string]string, mask *masker) map[string]string {
	public := make(map[string]string)
	for key, value := range outputs {
		secret := false
		for _, masked := range mask.values {
			if strings.Contains(key, masked) || strings.Contains(value, masked) {
				secret = true
				break
			}
		}
		if !secret {
			public[key] = value
		}
	}
	return public
}

func finalizeOutputs(plan run.Plan, env map[string]string, outputs map[string]map[string]string, steps map[string]run.StepResult, mask *masker, result *run.Result) error {
	// Do this even if a declared job output fails to expand. The result can still
	// be persisted after an executor error, and later masks cover earlier values.
	for index := range result.Steps {
		result.Steps[index].Outputs = publicOutputs(result.Steps[index].Outputs, mask)
	}
	if len(plan.Outputs) == 0 {
		return nil
	}
	if len(plan.Outputs) > maxOutputCount {
		return fmt.Errorf("job output count exceeds limit")
	}
	// A declared step that never started has empty properties at job completion.
	// During normal execution, future step references remain unavailable.
	allOutputs := make(map[string]map[string]string, len(plan.Steps))
	allSteps := make(map[string]run.StepResult, len(plan.Steps))
	for _, step := range plan.Steps {
		allOutputs[step.ID] = outputs[step.ID]
		allSteps[step.ID] = steps[step.ID]
	}
	jobOutputs := make(map[string]string, len(plan.Outputs))
	for _, key := range sortedMapKeys(plan.Outputs) {
		value, err := interpolate(plan.Outputs[key], plan, env, allOutputs, allSteps)
		if err != nil {
			return fmt.Errorf("evaluate job output: %w", err)
		}
		jobOutputs[key] = value
		if _, err := outputSize(jobOutputs); err != nil {
			return err
		}
	}
	result.Outputs = publicOutputs(jobOutputs, mask)
	return nil
}
