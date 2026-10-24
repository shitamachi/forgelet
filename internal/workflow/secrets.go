package workflow

import (
	"sort"
	"strings"

	"github.com/shitamachi/forgelet/internal/expression"
	"github.com/shitamachi/forgelet/internal/run"
)

func secretReferences(plan run.Plan) []string {
	names := map[string]bool{}
	inspect := func(value string) {
		_, _ = expression.Parts(value, func(source string) (string, error) {
			err := expression.Validate(source, func(ref string) bool {
				if strings.HasPrefix(ref, "secrets.") {
					names[strings.TrimPrefix(ref, "secrets.")] = true
				}
				return true
			})
			return "", err
		})
	}
	for _, value := range plan.Env {
		inspect(value)
	}
	for _, step := range plan.Steps {
		inspect(step.Run)
		inspect(step.WorkingDirectory)
		for _, value := range step.Env {
			inspect(value)
		}
		if step.Action != nil {
			for _, value := range step.Action.Inputs {
				inspect(value)
			}
		}
	}
	var result []string
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}
