package actworker

import (
	"strings"

	"github.com/nektos/act/pkg/model"

	"github.com/shitamachi/forgelet/internal/run"
)

// A profile supplies the execution environment's defaults; explicit workflow
// inputs retain precedence. The Action code still creates and cleans the builder.
func configureBuilderDefaults(wf *model.Workflow, request run.ActRequest) {
	for _, job := range wf.Jobs {
		for _, step := range job.Steps {
			if !strings.HasPrefix(step.Uses, "docker/setup-buildx-action@") {
				continue
			}
			if driver := step.With["driver"]; driver != "" && driver != "docker-container" {
				continue
			}
			if step.With == nil {
				step.With = map[string]string{}
			}
			if request.BuildkitImage != "" && !hasDriverImage(step.With["driver-opts"]) {
				options := step.With["driver-opts"]
				if options != "" {
					options += "\n"
				}
				step.With["driver-opts"] = options + "image=" + request.BuildkitImage
			}
			if request.BuildkitConfig != "" && step.With["buildkitd-config"] == "" && step.With["buildkitd-config-inline"] == "" {
				step.With["buildkitd-config-inline"] = request.BuildkitConfig
			}
		}
	}
}
func hasDriverImage(options string) bool {
	for _, line := range strings.FieldsFunc(options, func(r rune) bool { return r == '\n' || r == ',' }) {
		if strings.HasPrefix(strings.TrimSpace(line), "image=") {
			return true
		}
	}
	return false
}
