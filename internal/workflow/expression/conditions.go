package expression

import (
	"strings"
)

// EvaluateCondition evaluates a workflow `if:` value (spec 0006 V1).
//
//   - A value without `${{ }}` is treated as one whole expression;
//     following GitHub, conditions that call none of the status functions
//     implicitly become `success() && (<condition>)`.
//   - A `${{ }}` template is rendered first and the rendered text is then
//     evaluated as an expression.
//
// An empty condition is true. Errors are returned verbatim: callers decide
// whether an invalid condition skips or fails the job.
func EvaluateCondition(raw string, env *Env) (bool, error) {
	cond := strings.TrimSpace(raw)
	if cond == "" {
		return true, nil
	}
	if strings.Contains(cond, "${{") {
		rendered, err := Interpolate(cond, env)
		if err != nil {
			return false, err
		}
		cond = strings.TrimSpace(rendered)
		if cond == "" {
			return true, nil
		}
	} else if !mentionsStatusFunction(cond) {
		cond = "success() && (" + cond + ")"
	}
	v, err := Eval(cond, env)
	if err != nil {
		return false, err
	}
	return v.Truthy(), nil
}

func mentionsStatusFunction(cond string) bool {
	lower := strings.ToLower(cond)
	for _, fn := range []string{"success(", "failure(", "cancelled(", "always("} {
		if strings.Contains(lower, fn) {
			return true
		}
	}
	return false
}
