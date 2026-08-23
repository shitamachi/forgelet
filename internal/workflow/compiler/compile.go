// Package compiler turns the syntax AST into the provider-neutral compiled
// workflow (IR) and bridges it to scheduler job intents. Syntax nodes never
// leave the workflow module (spec 0001 FR-2.1).
package compiler

import (
	"fmt"
	"path"
	"strings"

	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/workflow/syntax"
)

// Step is the IR run step.
type Step struct {
	Name string
	Run  string
	Env  map[string]string
}

// JobInstance is one executable job of the compiled workflow. V1 matrix
// expansion produces multiple instances per workflow job with stable keys.
type JobInstance struct {
	Key         string // job id, or "test[go=1.27,os=linux]" with matrix
	DisplayName string
	RunnerClass string
	DependsOn   []string
	Matrix      map[string]string
	Env         map[string]string
	Steps       []Step
}

// Compiled is the compiled workflow. It carries the trigger filters needed
// for event matching but no syntax AST nodes.
type Compiled struct {
	Name string
	Jobs []JobInstance // document order

	push *syntax.PushTrigger
}

// Compile validates the AST semantics and produces the IR.
func Compile(wf *syntax.Workflow) (*Compiled, error) {
	if wf == nil {
		return nil, fmt.Errorf("compile: nil workflow")
	}
	if len(wf.Jobs) == 0 {
		return nil, fmt.Errorf("compile %s: no jobs", wf.File)
	}
	out := &Compiled{Name: wf.Name, Jobs: []JobInstance{}, push: wf.On.Push}

	ordered, err := topoSort(wf.Jobs)
	if err != nil {
		return nil, fmt.Errorf("compile %s: %w", wf.File, err)
	}
	for _, job := range ordered {
		if strings.TrimSpace(job.RunsOn) == "" {
			return nil, fmt.Errorf("compile %s: job %q: empty runs-on", wf.File, job.ID)
		}
		if len(job.Steps) == 0 {
			return nil, fmt.Errorf("compile %s: job %q: no steps", wf.File, job.ID)
		}
		inst := JobInstance{
			Key:         job.ID,
			DisplayName: displayName(job),
			RunnerClass: job.RunsOn,
			DependsOn:   job.Needs,
			Env:         job.Env,
		}
		if inst.Env == nil {
			inst.Env = map[string]string{}
		}
		for i, step := range job.Steps {
			if strings.TrimSpace(step.Run) == "" {
				return nil, fmt.Errorf("compile %s: job %q step %d: empty run", wf.File, job.ID, i)
			}
			inst.Steps = append(inst.Steps, Step{Name: step.Name, Run: step.Run, Env: step.Env})
		}
		expanded, err := expandMatrix(job, inst)
		if err != nil {
			return nil, fmt.Errorf("compile %s: %w", wf.File, err)
		}
		out.Jobs = append(out.Jobs, expanded...)
	}
	return out, nil
}

func displayName(job *syntax.Job) string {
	if job.Name != "" {
		return job.Name
	}
	return job.ID
}

// MatchesPush reports whether a push to ref triggers the workflow
// (FR-W2.3): no filters match everything; `branches`/`branches-ignore` use
// glob patterns on the branch short name. Patterns prefixed with `!` are
// exclusions and take precedence over inclusions. A workflow without a
// push trigger never matches.
func (c *Compiled) MatchesPush(ref string) bool {
	if c.push == nil {
		return false
	}
	return matchesFilters(c.push, syntax.TrimRefPrefix(ref))
}

// JobIntents bridges the compiled workflow to the scheduler Compiler port
// contract (FR-W2.4). PlanDigest is filled by the executor wiring (0008).
func (c *Compiled) JobIntents() []model.JobIntent {
	intents := make([]model.JobIntent, 0, len(c.Jobs))
	for _, job := range c.Jobs {
		intent := model.JobIntent{
			JobKey:      job.Key,
			RunnerClass: job.RunnerClass,
			DependsOn:   job.DependsOn,
		}
		if job.Matrix != nil {
			intent.Matrix = job.Matrix
		}
		intents = append(intents, intent)
	}
	return intents
}

func matchesFilters(trigger *syntax.PushTrigger, branch string) bool {
	for _, pattern := range trigger.BranchesIgnore {
		if matchGlob(pattern, branch) {
			return false
		}
	}
	include, exclude := splitExclusions(trigger.Branches)
	for _, pattern := range exclude {
		if matchGlob(pattern, branch) {
			return false
		}
	}
	if len(include) == 0 && len(exclude) == 0 {
		return true
	}
	if len(include) == 0 {
		// Only exclusions were declared: everything else matches.
		return true
	}
	for _, pattern := range include {
		if matchGlob(pattern, branch) {
			return true
		}
	}
	return false
}

func splitExclusions(patterns []string) (include, exclude []string) {
	for _, p := range patterns {
		if strings.HasPrefix(p, "!") {
			exclude = append(exclude, strings.TrimPrefix(p, "!"))
		} else {
			include = append(include, p)
		}
	}
	return include, exclude
}

func matchGlob(pattern, s string) bool {
	matched, err := path.Match(pattern, s)
	if err != nil {
		// Malformed patterns never match; the syntax layer is the place to
		// catch them in the future.
		return false
	}
	return matched
}
