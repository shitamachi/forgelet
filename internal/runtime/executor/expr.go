package executor

import (
	"runtime"

	"github.com/shitamachi/forgelet/internal/run/plan"
	"github.com/shitamachi/forgelet/internal/workflow/expression"
)

// stepState is one step's expression-visible record: its captured outputs
// and the GitHub outcome/conclusion pair.
type stepState struct {
	outputs    map[string]string
	outcome    string
	conclusion string
}

// Outcome and conclusion values visible through the steps context.
const (
	outcomeSuccess  = "success"
	outcomeFailure  = "failure"
	outcomeSkipped  = "skipped"
	conclusionOK    = "success"
	conclusionFail  = "failure"
	conclusionSkip  = "skipped"
	jobStatusOK     = "success"
	jobStatusFailed = "failure"
)

// githubOf exposes the plan header as the `github` expression context.
func githubOf(p *plan.Plan) expression.Value {
	return expression.ObjOf(map[string]expression.Value{
		"event_name": expression.StrOf(p.EventName),
		"actor":      expression.StrOf(p.Actor),
		"ref":        expression.StrOf(p.Ref),
		"sha":        expression.StrOf(p.SHA),
		"repository": expression.ObjOf(map[string]expression.Value{
			"owner":     expression.StrOf(p.Repository.Owner),
			"name":      expression.StrOf(p.Repository.Name),
			"full_name": expression.StrOf(p.Repository.Owner + "/" + p.Repository.Name),
		}),
	})
}

// exprEnv builds the runtime evaluation environment for a plan: github
// (from the plan header), env (accumulated), steps (outputs + results so
// far), job, runner, and hashFiles over the workspace (0007 T6 capability).
func (e *Engine) exprEnv(p *plan.Plan, env map[string]string, states map[string]*stepState) *expression.Env {
	envCtx := map[string]expression.Value{}
	for k, v := range env {
		envCtx[k] = expression.StrOf(v)
	}
	steps := map[string]expression.Value{}
	for id, st := range states {
		outputs := map[string]expression.Value{}
		for k, v := range st.outputs {
			outputs[k] = expression.StrOf(v)
		}
		steps[id] = expression.ObjOf(map[string]expression.Value{
			"outputs":    expression.ObjOf(outputs),
			"outcome":    expression.StrOf(st.outcome),
			"conclusion": expression.StrOf(st.conclusion),
		})
	}
	return expression.NewEnv().
		With("github", githubOf(p)).
		With("env", expression.ObjOf(envCtx)).
		With("steps", expression.ObjOf(steps)).
		With("job", expression.ObjOf(map[string]expression.Value{
			"status": expression.StrOf(jobStatusFor(states)),
		})).
		With("runner", expression.ObjOf(map[string]expression.Value{
			"os":   expression.StrOf(runtime.GOOS),
			"arch": expression.StrOf(runtime.GOARCH),
		})).
		WithWorkspaceHasher(expression.NewDirHasher(e.WorkDir))
}

// jobStatusFor mirrors GitHub's in-job job.status: success until a step
// failed without continue-on-error.
func jobStatusFor(states map[string]*stepState) string {
	for _, st := range states {
		if st.outcome == outcomeFailure && st.conclusion != conclusionOK {
			return jobStatusFailed
		}
	}
	return jobStatusOK
}
