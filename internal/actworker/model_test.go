package actworker

import (
	"strings"
	"testing"

	"github.com/nektos/act/pkg/model"
	"github.com/nektos/act/pkg/runner"
	"gopkg.in/yaml.v3"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestResolvedContextsAndDependencyAliases(t *testing.T) {
	p := run.Plan{Key: "call.child", Inputs: map[string]any{"postgres": true, "count": 2, "name": "owner's inputs"}, ParentGate: "gate", NeedsAliases: map[string]string{"call.build": "build"}, NeedsContext: map[string]run.Dependency{"gate": {Result: run.Succeeded}, "call.build": {Result: run.Succeeded, Outputs: map[string]string{"digest": "immutable"}}}, Steps: []run.Step{{ID: "check", Run: "echo '${{ inputs.postgres }}'"}}}
	wf, err := workflow(p)
	if err != nil {
		t.Fatal(err)
	}
	if wf.Jobs["gate"] != nil || wf.Jobs["build"] == nil || len(wf.Jobs) != 2 {
		t.Fatal(wf.Jobs)
	}
	rc := &runner.RunContext{Config: &runner.Config{Workdir: t.TempDir(), EventName: "push", Env: map[string]string{}, Secrets: map[string]string{}, Platforms: map[string]string{"forgelet": "-self-hosted"}}, Run: &model.Run{Workflow: wf, JobID: localKey(p)}}
	for _, entry := range []struct {
		expression string
		want       any
	}{{"${{ inputs.postgres }}", true}, {"${{ inputs.name }}", "owner's inputs"}, {"${{ needs.build.outputs.digest }}", "immutable"}} {
		node := yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: freeze(entry.expression, p)}
		if err := rc.NewExpressionEvaluator(t.Context()).EvaluateYamlNode(t.Context(), &node); err != nil {
			t.Fatal(err)
		}
		var got any
		if err := node.Decode(&got); err != nil || got != entry.want {
			t.Fatalf("%s: got %#v (%v), want %#v", entry.expression, got, err, entry.want)
		}
	}
	for _, expr := range []string{"${{ inputs.postgres }}", "${{ inputs['count'] }}", "${{ toJSON(inputs) }}", "${{ inputs.name }}"} {
		x := freeze(expr, p)
		if !strings.Contains(x, "fromJSON(") || strings.Contains(x, "${{ inputs") {
			t.Fatal(x)
		}
	}
	for _, expr := range []string{"${{ 'inputs.postgres' }}", "${{ github.event.inputs.postgres }}", "literal inputs"} {
		if got := freeze(expr, p); got != expr {
			t.Fatalf("rewrote unrelated data: %s", got)
		}
	}
}

func TestReusableJobUsesOriginalJobContext(t *testing.T) {
	p := run.Plan{Key: "call/reliability[0]", Group: "call/reliability", Steps: []run.Step{{ID: "check", Run: "echo job"}}}
	wf, err := workflow(p)
	if err != nil || wf.Jobs["reliability"] == nil || localKey(p) != "reliability" {
		t.Fatal(wf, err)
	}
}

func TestNewPlansCannotRunMutableOrMismatchedSources(t *testing.T) {
	for _, sha := range []string{"", "main", strings.Repeat("b", 40)} {
		p := run.Plan{Key: "job", ActionPins: map[string]string{"owner/repo@v1": strings.Repeat("a", 40)}, Steps: []run.Step{{Action: &run.Action{Uses: "owner/repo@v1", Repository: "owner/repo", Ref: "v1", SHA: sha}}}}
		if _, err := workflow(p); err == nil {
			t.Fatal("accepted unpinned or mismatched action", sha)
		}
	}
}

func TestConfiguredGitHubAPIContextMatchesRuntimeEnvironment(t *testing.T) {
	p := run.Plan{Key: "job", GitHubAPIURL: "https://api-proxy.example/v3", Repository: "owner/repo", Steps: []run.Step{{ID: "test", Run: "echo api"}}}
	wf, err := workflow(p)
	if err != nil {
		t.Fatal(err)
	}
	env := runtimeEnvironment(run.ActRequest{Plan: p})
	rc := &runner.RunContext{Config: &runner.Config{GitHubInstance: "github.com", Workdir: t.TempDir(), Env: env}, Run: &model.Run{Workflow: wf, JobID: "job"}}
	node := yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "${{ github.api_url }}"}
	if err := rc.NewExpressionEvaluator(t.Context()).EvaluateYamlNode(t.Context(), &node); err != nil {
		t.Fatal(err)
	}
	if node.Value != p.GitHubAPIURL || env["GITHUB_API_URL"] != p.GitHubAPIURL {
		t.Fatal(node, env)
	}
}
