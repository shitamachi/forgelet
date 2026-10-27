package storage

import (
	"fmt"
	"strings"

	"github.com/shitamachi/forgelet/internal/expression"
	"github.com/shitamachi/forgelet/internal/run"
)

// resolveCall is part of the same SQLite transaction as ordinary scheduling.
// Gates/results are durable graph nodes, never executor reservations.
func resolveCall(job *run.Job, jobs map[string]*run.Job, eval expression.Context) (bool, error) {
	p := &job.Plan
	if p.ParentGate != "" {
		gate := jobs[p.ParentGate]
		if gate.State != run.Succeeded {
			state := run.Skipped
			if p.Control == "call_result" {
				state = gate.State
			}
			job.State = state
			job.Result = &run.Result{State: state}
			return true, nil
		}
		p.Inputs = gate.Plan.CallValues
		// dependencyContext was constructed before the resolved inputs were applied.
		for name, value := range p.Inputs {
			eval.Typed["inputs."+name] = value
		}
		eval.Typed["inputs"] = p.Inputs
	}
	if p.Control == "call_gate" {
		allowed, err := expression.Evaluate(p.If, eval)
		if err != nil {
			return false, err
		}
		if !allowed {
			job.State = run.Skipped
			job.Result = &run.Result{State: run.Skipped}
			return true, nil
		}
		inputs := map[string]any{}
		for name, spec := range p.CallInputs {
			value := spec.Value
			if text, ok := value.(string); ok && strings.Contains(text, "${{") {
				if spec.Template {
					value, err = expression.Interpolate(text, eval)
				} else {
					value, err = expression.Eval(text, eval)
				}
				if err != nil {
					return false, fmt.Errorf("workflow input %s: %w", name, err)
				}
			}
			if err = run.ValidateCallInput(spec.Type, value); err != nil {
				return false, fmt.Errorf("workflow input %s: %w", name, err)
			}
			inputs[name] = value
		}
		p.CallValues = inputs
		job.State = run.Succeeded
		job.Result = &run.Result{State: run.Succeeded}
		return true, nil
	}
	if p.Control == "call_result" {
		state := run.Skipped
		for _, key := range p.Needs {
			if key == p.ParentGate {
				continue
			}
			dep := jobs[key]
			switch dep.State {
			case run.Failed, run.Interrupted, run.TimedOut:
				state = run.Failed
			case run.Cancelled:
				if state != run.Failed {
					state = run.Cancelled
				}
			case run.Succeeded:
				if state == run.Skipped {
					state = run.Succeeded
				}
			}
			local := p.NeedsAliases[key]
			if dep.Result != nil {
				for name, value := range dep.Result.Outputs {
					eval.Values["jobs."+local+".outputs."+name] = value
				}
			}
		}
		outputs := map[string]string{}
		size := 0
		for name, text := range p.Outputs {
			value, err := expression.Interpolate(text, eval)
			if err != nil {
				return false, fmt.Errorf("workflow output %s: %w", name, err)
			}
			if strings.ContainsRune(value, 0) {
				return false, fmt.Errorf("workflow output contains NUL")
			}
			size += len(name) + len(value)
			if size > 64<<10 {
				return false, fmt.Errorf("workflow outputs exceed 64 KiB")
			}
			outputs[name] = value
		}
		job.State = state
		job.Result = &run.Result{State: state, Outputs: outputs}
		return true, nil
	}
	return false, nil
}
