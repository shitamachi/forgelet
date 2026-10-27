package workflow

import (
	"reflect"
	"strings"
	"testing"

	"github.com/shitamachi/forgelet/internal/action"
	"github.com/shitamachi/forgelet/internal/run"
)

func deploymentPlans(t *testing.T) []run.Plan {
	t.Helper()
	a, err := action.Reference("docker/build-push-action@v6")
	if err != nil {
		t.Fatal(err)
	}
	return []run.Plan{
		{RunID: "run", JobID: "ci", Key: "test[0]", Group: "test", MatrixTotal: 2},
		{RunID: "run", JobID: "ci2", Key: "test[1]", Group: "test", MatrixTotal: 2},
		{RunID: "run", JobID: "publish", Key: "image", Group: "image", Repository: "me/repo", SHA: strings.Repeat("a", 40), Ref: "refs/heads/main", EventName: "push", SourceWorkflow: ".github/workflows/check.yml", Kernel: "act", Profile: "actions", TimeoutSeconds: 1200, Outputs: map[string]string{"existing": "value"}, Env: map[string]string{"PRIVATE": "${{ secrets.PRIVATE }}"}, SecretNames: []string{"PRIVATE"}, Steps: []run.Step{{ID: "__step_1", Action: a}}},
	}
}

func TestAttachDeploymentsPreservesSourceAndWaitsForEveryGroup(t *testing.T) {
	plans := deploymentPlans(t)
	got, err := AttachDeployments(plans, []DeploymentBinding{{Target: "platform-web", Job: "image", Profile: "native"}})
	if err != nil {
		t.Fatal(err)
	}
	d := got[3]
	if len(got) != 4 || !reflect.DeepEqual(d.Needs, []string{"image", "test"}) || d.Kernel != "" || d.Profile != "native" || d.Group != d.Key || len(d.JobID) != 24 || d.JobID == plans[2].JobID {
		t.Fatal(got)
	}
	if d.Repository != plans[2].Repository || d.SHA != plans[2].SHA || d.SourceWorkflow != plans[2].SourceWorkflow || d.Permissions["contents"] != "none" || len(d.Env) != 0 || len(d.SecretNames) != 0 || d.If != "" {
		t.Fatal("deployment context/permission leak", d)
	}
	if !reflect.DeepEqual(plans[2].Steps, got[2].Steps) || len(plans[2].Outputs) != 1 || got[2].Outputs["existing"] != "value" {
		t.Fatal("source plan mutated")
	}
	if got[2].Outputs["forgelet_deploy_platform_web_digest"] != "${{ steps['__step_1'].outputs.digest }}" || d.Steps[0].Action.Inputs["digest"] != "${{ needs['image'].outputs['forgelet_deploy_platform_web_digest'] }}" {
		t.Fatal("digest not linked", got)
	}
	again, err := AttachDeployments(plans, []DeploymentBinding{{Target: "platform-web", Job: "image", Profile: "native"}})
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatal("non deterministic graph", err)
	}
}

func TestAttachDeploymentsRejectsAmbiguousPublishers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  func([]run.Plan) []run.Plan
		binding DeploymentBinding
	}{
		{"missing", func(p []run.Plan) []run.Plan { return p[:2] }, DeploymentBinding{"web", "image", "native"}},
		{"matrix", func(p []run.Plan) []run.Plan { p[2].MatrixTotal = 2; return p }, DeploymentBinding{"web", "image", "native"}},
		{"multiple steps", func(p []run.Plan) []run.Plan { p[2].Steps = append(p[2].Steps, p[2].Steps[0]); return p }, DeploymentBinding{"web", "image", "native"}},
		{"control", func(p []run.Plan) []run.Plan { p[2].Control = "result"; return p }, DeploymentBinding{"web", "image", "native"}},
		{"job collision", func(p []run.Plan) []run.Plan { return append(p, run.Plan{Key: "forgelet_deploy_web"}) }, DeploymentBinding{"web", "image", "native"}},
		{"output collision", func(p []run.Plan) []run.Plan { p[2].Outputs["forgelet_deploy_web_digest"] = "reserved"; return p }, DeploymentBinding{"web", "image", "native"}},
		{"invalid", func(p []run.Plan) []run.Plan { return p }, DeploymentBinding{"web", "image", ""}},
		{"expanded limit", func(p []run.Plan) []run.Plan { return append(p, make([]run.Plan, 253)...) }, DeploymentBinding{"web", "image", "native"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := AttachDeployments(tc.change(deploymentPlans(t)), []DeploymentBinding{tc.binding}); err == nil {
				t.Fatal("invalid binding admitted")
			}
		})
	}
}
