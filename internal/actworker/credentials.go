package actworker

import (
	"strings"

	"github.com/nektos/act/pkg/model"

	"github.com/shitamachi/forgelet/internal/run"
)

func executionSecrets(wf *model.Workflow, request run.ActRequest) map[string]string {
	secrets := map[string]string{}
	for k, v := range request.Secrets {
		secrets[k] = v
	}
	if request.Token != "" {
		secrets["GITHUB_TOKEN"] = request.Token
	}
	if request.PackageToken != "" {
		secrets["GITHUB_TOKEN"] = request.PackageToken
		// Preserve the source-only credential for implicit checkout even when the
		// original workflow uses secrets.GITHUB_TOKEN to publish packages.
		for _, job := range wf.Jobs {
			for _, step := range job.Steps {
				if strings.HasPrefix(step.Uses, "actions/checkout@") && step.With["token"] == "" {
					if step.With == nil {
						step.With = map[string]string{}
					}
					step.With["token"] = request.Token
				}
			}
		}
	}
	return secrets
}
