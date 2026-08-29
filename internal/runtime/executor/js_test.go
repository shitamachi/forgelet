package executor

import (
	"context"
	"log/slog"
	"testing"

	"github.com/shitamachi/forgelet/internal/run/plan"
)

func TestJSHandlerGithubScript(t *testing.T) {
	// Test the JS handler for actions/github-script with core.getInput/setOutput
	bc := BuiltinContext{
		Ctx:       context.Background(),
		Workspace: t.TempDir(),
		Inputs: map[string]string{
			"script": "core.setOutput('greeting', 'hello ' + core.getInput('name'))",
			"name":   "forgelet",
		},
		Env:    map[string]string{},
		Logger: slog.Default(),
	}
	outputs := map[string]string{}
	bc.SetOutput = func(k, v string) { outputs[k] = v }

	// Simulate a JSStep for actions/github-script
	js := &plan.JSStep{
		Repo:   "actions/github-script",
		Ref:    "v6",
		Inputs: map[string]string{"script": bc.Inputs["script"], "name": "forgelet"},
		Script: bc.Inputs["script"],
	}
	// Call runJS directly with the script and JSStep
	err := runJS(context.Background(), bc, bc.Inputs["script"], js)
	if err != nil {
		t.Fatalf("runJS: %v", err)
	}
	if outputs["greeting"] != "hello forgelet" {
		t.Errorf("output greeting = %q, want hello forgelet", outputs["greeting"])
	}
}
