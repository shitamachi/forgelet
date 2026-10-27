package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestExecuteFailureConditionsAndSkippedSteps(t *testing.T) {
	plan := run.Plan{Steps: []run.Step{
		{ID: "build", Run: "exit 7"},
		{ID: "default", Run: "echo unexpected"},
		{ID: "implicit", If: "github.ref == 'refs/heads/main'", Run: "echo unexpected"},
		{ID: "cleanup", If: "failure()", Run: "echo cleanup"},
		{ID: "always", If: "always() && steps.build.outcome == 'failure' && steps.cleanup.conclusion == 'success'", Run: "echo always"},
		{ID: "success", If: "success()", Run: "echo unexpected"},
	}, Ref: "refs/heads/main"}
	var logs bytes.Buffer
	result, err := Execute(t.Context(), plan, Options{Workspace: t.TempDir(), Log: func(_ string, data []byte) error { _, err := logs.Write(data); return err }})
	if err != nil || result.State != run.Failed || len(result.Steps) != len(plan.Steps) || logs.String() != "cleanup\nalways\n" {
		t.Fatalf("result=%+v err=%v logs=%q", result, err, logs.String())
	}
	for _, index := range []int{1, 2, 5} {
		if result.Steps[index].State != run.Skipped || result.Steps[index].Outcome != run.Skipped {
			t.Fatalf("step %d did not record skipped conclusion and outcome: %+v", index, result.Steps[index])
		}
	}
}

func TestExecuteContinueOnErrorPreservesOutcome(t *testing.T) {
	plan := run.Plan{Steps: []run.Step{
		{ID: "optional", ContinueOnError: true, Run: "printf 'value=from-failure\\n' >> \"$GITHUB_OUTPUT\"\nexit 9"},
		{ID: "following", Run: "test '${{ steps.optional.outcome }}' = failure\ntest '${{ steps.optional.conclusion }}' = success\ntest '${{ steps.optional.outputs.value }}' = from-failure"},
		{ID: "failure", If: "failure()", Run: "exit 1"},
		{ID: "inspect", If: "steps.optional.outcome == 'failure' && steps.optional.conclusion == 'success'", Run: "true"},
	}, Outputs: map[string]string{"value": "${{ steps.optional.outputs.value }}", "outcome": "${{ steps.optional.outcome }}", "conclusion": "${{ steps.optional.conclusion }}"}}
	result, err := Execute(t.Context(), plan, Options{Workspace: t.TempDir()})
	if err != nil || result.State != run.Succeeded || len(result.Steps) != 4 || result.Steps[0].Outcome != run.Failed || result.Steps[0].State != run.Succeeded || result.Steps[0].ExitCode != 9 || result.Steps[2].State != run.Skipped || result.Steps[3].State != run.Succeeded {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if result.Outputs["value"] != "from-failure" || result.Outputs["outcome"] != "failure" || result.Outputs["conclusion"] != "success" {
		t.Fatalf("outputs=%+v", result.Outputs)
	}
}

func TestExecuteNeedsContextAndJobOutputs(t *testing.T) {
	for _, state := range []run.State{run.Succeeded, run.Failed, run.Skipped, run.TimedOut, run.Interrupted} {
		t.Run(string(state), func(t *testing.T) {
			want := contextResult(state)
			plan := run.Plan{Needs: []string{"build"}, NeedsContext: map[string]run.Dependency{
				"build":    {Result: state, Outputs: map[string]string{"tag": "v2"}},
				"indirect": {Result: run.Succeeded, Outputs: map[string]string{"secret": "not-exposed"}},
			}, Env: map[string]string{"TAG": "${{ needs.build.outputs.tag }}", "UPSTREAM": "${{ needs.build.result }}"}, Steps: []run.Step{
				{ID: "skipped", If: "false", Run: "echo unexpected"},
				{ID: "publish", If: "needs.build.outputs.tag == 'v2' && needs.build.result == '" + want + "' && steps.skipped.conclusion == 'skipped' && needs.indirect.outputs.secret == ''", Run: "test \"$TAG\" = v2\ntest \"$UPSTREAM\" = '" + want + "'\ntest '${{ steps.skipped.outputs.missing }}' = ''\nprintf 'tag=%s\\n' '${{ needs.build.outputs.tag }}' >> \"$GITHUB_OUTPUT\""},
			}, Outputs: map[string]string{"tag": "${{ steps.publish.outputs.tag }}", "skipped": "${{ steps.skipped.outputs.missing }}", "need": "${{ needs.build.result }}"}}
			result, err := Execute(t.Context(), plan, Options{Workspace: t.TempDir()})
			if err != nil || result.State != run.Succeeded || result.Steps[1].State != run.Succeeded || result.Outputs["tag"] != "v2" || result.Outputs["skipped"] != "" || result.Outputs["need"] != want {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestExecuteAncestorFailureAndEnvironmentConditions(t *testing.T) {
	plan := run.Plan{AncestorFailed: true, Env: map[string]string{"SCOPE": "job"}, Steps: []run.Step{
		{ID: "env", Run: "printf 'NEXT=yes\\n' >> \"$GITHUB_ENV\""},
		{ID: "failure", If: "failure() && env.NEXT == 'yes' && env.SCOPE == 'step'", Env: map[string]string{"SCOPE": "step"}, Run: "test \"$SCOPE\" = step"},
		{ID: "success", If: "success()", Run: "true"},
		{ID: "default", Run: "true"},
	}}
	result, err := Execute(t.Context(), plan, Options{Workspace: t.TempDir()})
	if err != nil || result.State != run.Succeeded || len(result.Steps) != len(plan.Steps) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, step := range result.Steps {
		if step.State != run.Succeeded {
			t.Fatalf("step did not execute: %+v", step)
		}
	}
}

func TestExecuteStepConditionSeesLocalEnvironmentOverrides(t *testing.T) {
	plan := run.Plan{Env: map[string]string{"ENABLED": "false", "OUTER": ""}, Steps: []run.Step{
		{ID: "override", If: "env.ENABLED == 'true' && env.OUTER == 'false'", Env: map[string]string{"ENABLED": "true", "OUTER": "${{ env.ENABLED }}"}, Run: "test \"$ENABLED\" = true\ntest \"$OUTER\" = false\necho override"},
		{ID: "following", If: "env.ENABLED == 'false' && env.OUTER == ''", Run: "test \"$ENABLED\" = false\ntest -z \"${OUTER:-}\"\necho outer"},
	}}
	var logs bytes.Buffer
	result, err := Execute(t.Context(), plan, Options{Workspace: t.TempDir(), Log: func(_ string, data []byte) error { _, err := logs.Write(data); return err }})
	if err != nil || result.State != run.Succeeded || len(result.Steps) != 2 || result.Steps[0].State != run.Succeeded || result.Steps[1].State != run.Succeeded || logs.String() != "override\nouter\n" {
		t.Fatalf("result=%+v err=%v logs=%q", result, err, logs.String())
	}
}

func TestExecuteTimeoutCleanupAndCancellationBoundaries(t *testing.T) {
	for _, kind := range []string{"step timeout", "job timeout", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			workspace := t.TempDir()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			plan := run.Plan{Steps: []run.Step{
				{ID: "wait", Run: "echo started\nsleep 30", ContinueOnError: true},
				{ID: "default", Run: "touch unexpected"},
				{ID: "cleanup", If: "failure() && steps.wait.outcome == 'failure'", Run: "touch cleanup"},
				{ID: "always", If: "always()", Run: "touch always"},
			}, Outputs: map[string]string{"missing": "${{ steps.always.outputs.missing }}"}}
			if kind == "step timeout" {
				plan.Steps[0].TimeoutSeconds = 1
			}
			if kind == "job timeout" {
				plan.TimeoutSeconds = 1
			}
			result, err := Execute(ctx, plan, Options{Workspace: workspace, Log: func(_ string, _ []byte) error {
				if kind == "cancelled" {
					cancel()
				}
				return nil
			}})
			want := run.TimedOut
			if kind == "cancelled" {
				want = run.Cancelled
			}
			if err != nil || result.State != want {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if _, err := os.Stat(filepath.Join(workspace, "unexpected")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("default step ran after timeout: %v", err)
			}
			if kind == "step timeout" {
				if len(result.Steps) != 4 || result.Steps[0].Outcome != run.TimedOut || result.Steps[0].State != run.TimedOut || result.Steps[2].State != run.Succeeded || result.Steps[3].State != run.Succeeded {
					t.Fatalf("step timeout cleanup=%+v", result)
				}
			} else {
				if len(result.Steps) != 1 {
					t.Fatalf("started cleanup after job termination: %+v", result)
				}
				for _, name := range []string{"cleanup", "always"} {
					if _, err := os.Stat(filepath.Join(workspace, name)); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("%s started after termination: %v", name, err)
					}
				}
			}
		})
	}
}

func TestExecuteContinueOnErrorDoesNotHideInfrastructureFailure(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		log          func(string, []byte) error
	}{
		{name: "malformed output", script: "echo malformed >> \"$GITHUB_OUTPUT\"\nexit 1"},
		{name: "reserved environment", script: "echo GITHUB_TOKEN=forged >> \"$GITHUB_ENV\"\nexit 1"},
		{name: "logging", script: "echo log\nsleep 30", log: func(string, []byte) error { return errors.New("log unavailable") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			started := time.Now()
			result, err := Execute(t.Context(), run.Plan{Steps: []run.Step{{ID: "fail", ContinueOnError: true, Run: tc.script}, {ID: "always", If: "always()", Run: "true"}}}, Options{Workspace: t.TempDir(), Log: tc.log})
			if err == nil || result.State != run.Failed || len(result.Steps) != 1 || result.Steps[0].State != run.Failed || time.Since(started) > 3*time.Second {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestExecuteContinueOnErrorCarriesInfrastructureError(t *testing.T) {
	// A job with continue-on-error must still expose executor errors. Otherwise
	// storage.Finish sees Failed + empty Error and converts the job to success.
	plan := run.Plan{ContinueOnError: true, Steps: []run.Step{
		{ID: "bad", Run: `printf 'bad=notjson\n' >> "$GITHUB_OUTPUT"`},
		{ID: "boom", Env: map[string]string{"X": `${{ fromJSON(steps.bad.outputs.bad) }}`}, Run: "true"},
	}}
	result, err := Execute(t.Context(), plan, Options{Workspace: t.TempDir()})
	if err == nil || result.State != run.Failed || result.Error == "" {
		t.Fatalf("result=%+v err=%v; infrastructure error must be visible", result, err)
	}
}

func TestExecuteOutputsAreBounded(t *testing.T) {
	var many strings.Builder
	for index := range 101 {
		fmt.Fprintf(&many, "o%d=v\n", index)
	}
	for _, tc := range []struct {
		name    string
		steps   []run.Step
		outputs map[string]string
	}{
		{name: "count", steps: []run.Step{{ID: "many", Run: "printf '" + many.String() + "' >> \"$GITHUB_OUTPUT\""}}},
		{name: "bytes", steps: []run.Step{{ID: "big", Run: "printf 'x=" + strings.Repeat("a", maxOutputBytes) + "\\n' >> \"$GITHUB_OUTPUT\""}}},
		{name: "combined steps", steps: []run.Step{{ID: "one", Run: "printf 'x=" + strings.Repeat("a", 40000) + "\\n' >> \"$GITHUB_OUTPUT\""}, {ID: "two", Run: "printf 'x=" + strings.Repeat("b", 40000) + "\\n' >> \"$GITHUB_OUTPUT\""}}},
		{name: "expanded job output", steps: []run.Step{{ID: "one", Run: "printf 'x=" + strings.Repeat("a", 30000) + "\\n' >> \"$GITHUB_OUTPUT\""}}, outputs: map[string]string{"x": "${{ steps.one.outputs.x }}${{ steps.one.outputs.x }}${{ steps.one.outputs.x }}"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := Execute(t.Context(), run.Plan{Steps: tc.steps, Outputs: tc.outputs}, Options{Workspace: t.TempDir()})
			if err == nil || result.State != run.Failed || len(result.Outputs) != 0 {
				t.Fatalf("state=%s outputs=%d err=%v", result.State, len(result.Outputs), err)
			}
		})
	}
}

func TestExecuteResultOutputsDoNotPublishMaskedSecrets(t *testing.T) {
	plan := run.Plan{Steps: []run.Step{
		{ID: "produce", Run: "printf 'safe=public\\nsecret=static-secret\\nlate=dynamic-secret\\ntoken=clone-secret\\nstatic-secret=value\\n' >> \"$GITHUB_OUTPUT\""},
		{ID: "mask", Run: "printf '::add-mask::dynamic-secret\\n'\ntest '${{ steps.produce.outputs.late }}' = dynamic-secret"},
	}, Outputs: map[string]string{
		"safe":          "${{ steps.produce.outputs.safe }}",
		"static":        "${{ steps.produce.outputs.secret }}",
		"dynamic":       "prefix-${{ steps.produce.outputs.late }}",
		"token":         "${{ steps.produce.outputs.token }}",
		"static-secret": "plain",
	}}
	result, err := Execute(t.Context(), plan, Options{Workspace: t.TempDir(), Masks: []string{"static-secret"}, CloneToken: "clone-secret"})
	if err != nil || result.State != run.Succeeded || len(result.Outputs) != 1 || result.Outputs["safe"] != "public" || len(result.Steps[0].Outputs) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"static-secret", "dynamic-secret", "clone-secret"} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatalf("result contains masked secret: %s", data)
		}
	}
}

func TestRuntimeContextRejectsUnavailableDependenciesAndSteps(t *testing.T) {
	plan := run.Plan{Needs: []string{"direct"}, NeedsContext: map[string]run.Dependency{"indirect": {Result: run.Succeeded}}}
	for _, value := range []string{"${{ needs.direct.result }}", "${{ needs.indirect.result }}", "${{ needs.indirect.outputs.tag }}", "${{ steps.future.outcome }}", "${{ steps.future.conclusion }}", "${{ needs.direct.unknown }}"} {
		if _, err := interpolate(value, plan, nil, nil); err == nil {
			t.Fatalf("accepted unavailable context %s", value)
		}
	}
	if _, err := interpolate(strings.Repeat("x", (1<<20)+1), plan, nil, nil); err == nil {
		t.Fatal("accepted oversized literal")
	}
}

func TestInterpolateEventFiltersAndNamespacedNeeds(t *testing.T) {
	plan := run.Plan{Group: "caller/emit", Repository: "me/repo", Event: []byte(`{"labels":[{"name":"bug"}]}`), Needs: []string{"caller/build", "$start/caller"}, ParentGate: "$start/caller", NeedsAliases: map[string]string{"caller/build": "build"}, NeedsContext: map[string]run.Dependency{"caller/build": {Result: run.Succeeded, Outputs: map[string]string{"value": "ok"}}, "$start/caller": {Result: run.Succeeded, Outputs: map[string]string{"hidden": "forbidden"}}}}
	got, err := interpolate("${{ github.job }}:${{ contains(github.event.labels.*.name, 'bug') }}:${{ contains(needs.*.result, 'success') }}:${{ needs['build'].outputs.value }}", plan, nil, nil)
	if err != nil || got != "emit:true:true:ok" {
		t.Fatal(got, err)
	}
	if _, ok := directDependency(plan, "$start/caller"); ok {
		t.Fatal("control gate exposed as a user dependency")
	}
}
