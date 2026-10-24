package executor

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shitamachi/forgelet/internal/run"
	"github.com/shitamachi/forgelet/internal/workflow"
)

func node24(t *testing.T) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node 24 is tested in the mandatory Testcontainers suite")
	}
	v, err := exec.CommandContext(t.Context(), node, "--version").Output()
	if err != nil || !strings.HasPrefix(string(v), "v24.") {
		t.Skip("local Node is not version 24; Testcontainers provides Node 24")
	}
	return node
}
func writeActionFile(t *testing.T, root, name, body string) {
	t.Helper()
	filename := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRealNodeCompositeStateOutputsAndReversePosts(t *testing.T) {
	node := node24(t)
	workspace := t.TempDir()
	writeActionFile(t, workspace, "node/action.yml", `name: Test Node
description: lifecycle
inputs:
  value: {default: default}
runs:
  using: node24
  pre: pre.js
  main: main.js
  post: post.js
`)
	writeActionFile(t, workspace, "node/pre.js", `const fs=require('fs');fs.appendFileSync('trace','pre-'+process.env.INPUT_VALUE+'\n');fs.appendFileSync(process.env.GITHUB_STATE,'from_pre='+process.env.INPUT_VALUE+'\n');`)
	writeActionFile(t, workspace, "node/main.js", `const fs=require('fs');if(process.env.STATE_from_pre!==process.env.INPUT_VALUE)process.exit(4);fs.appendFileSync('trace','main-'+process.env.INPUT_VALUE+'\n');fs.appendFileSync(process.env.GITHUB_OUTPUT,'answer='+process.env.INPUT_VALUE+'\n');fs.appendFileSync(process.env.GITHUB_ENV,'SHARED=from-node\n');fs.appendFileSync(process.env.GITHUB_STATE,'from_main=yes\n');console.log('node-'+process.env.INPUT_VALUE);`)
	writeActionFile(t, workspace, "node/post.js", `const fs=require('fs');if(process.env.STATE_from_main!=='yes')process.exit(5);fs.appendFileSync('trace','post-'+process.env.INPUT_VALUE+'\n');console.log('post-'+process.env.INPUT_VALUE);`)
	writeActionFile(t, workspace, "composite/action.yml", `name: Composite
description: scoped inputs and output
inputs:
  value: {default: composite}
outputs:
  result:
    value: ${{ steps.inner.outputs.answer }}
runs:
  using: composite
  steps:
    - id: inner
      uses: ./node
      with:
        value: ${{ inputs.value }}
    - shell: sh
      run: |
        test "$SHARED" = from-node
        test '${{ toJSON(inputs) }}' = '{"value":"second"}'
`)
	plans, err := workflow.Compile([]byte(`on: push
jobs:
  action:
    runs-on: test
    steps:
      - id: first
        uses: ./node
        with: {value: first}
      - id: second
        uses: ./composite
        with: {value: second}
      - shell: sh
        run: |
          test '${{ steps.first.outputs.answer }}' = first
          test '${{ steps.second.outputs.result }}' = second
          test "$SHARED" = from-node
          test -z "${INPUT_VALUE:-}"
          exit 9
      - if: failure()
        shell: sh
        run: echo cleanup
`), run.Event{}, "actions")
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	result, err := Execute(t.Context(), plans[0], Options{Workspace: workspace, NodeRuntimes: map[string]string{"node24": node}, Log: func(_ string, data []byte) error { _, err := logs.Write(data); return err }})
	if err != nil || result.State != run.Failed || !strings.Contains(logs.String(), "cleanup") {
		t.Fatalf("result=%+v err=%v logs=%s", result, err, logs.String())
	}
	trace, err := os.ReadFile(filepath.Join(workspace, "trace"))
	if err != nil {
		t.Fatal(err)
	}
	if string(trace) != "pre-first\nmain-first\npre-second\nmain-second\npost-second\npost-first\n" {
		t.Fatalf("trace=%s", trace)
	}
}
func TestActionRejectsUnknownInputAndScriptEscape(t *testing.T) {
	node := node24(t)
	for _, kind := range []string{"input", "script", "cycle"} {
		t.Run(kind, func(t *testing.T) {
			workspace := t.TempDir()
			metadata := "name: Test\ndescription: Test\nruns: {using: node24, main: main.js}\n"
			a := &run.Action{Path: "a", Inputs: map[string]string{}}
			if kind == "input" {
				a.Inputs["unknown"] = "bad"
			}
			if kind == "script" {
				metadata = "name: Test\ndescription: Test\nruns: {using: node24, main: ../main.js}\n"
			}
			if kind == "cycle" {
				metadata = "name: Test\ndescription: Test\nruns:\n  using: composite\n  steps: [{uses: ./a}]\n"
			}
			writeActionFile(t, workspace, "a/action.yml", metadata)
			writeActionFile(t, workspace, "a/main.js", "throw new Error('must not run')")
			_, err := Execute(t.Context(), run.Plan{Steps: []run.Step{{ID: "action", Action: a}}}, Options{Workspace: workspace, NodeRuntimes: map[string]string{"node24": node}})
			if err == nil {
				t.Fatal("accepted invalid action")
			}
		})
	}
}

func TestNodeActionPostConditionsFailuresAndCancellation(t *testing.T) {
	node := node24(t)
	for _, tc := range []struct {
		name, main, post, postIf string
		cancel                   bool
		timeout                  int
		want                     run.State
		ranPost                  bool
	}{
		{name: "post failure", main: "console.log('main')", post: "console.log('post');process.exit(5)", want: run.Failed, ranPost: true},
		{name: "post if failure skips success", main: "console.log('main')", post: "console.log('post')", postIf: "failure()", want: run.Succeeded},
		{name: "post if failure runs", main: "process.exit(7)", post: "console.log('post')", postIf: "failure()", want: run.Failed, ranPost: true},
		{name: "cancellation stops new post", main: "console.log('ready');setInterval(()=>{},1000)", post: "console.log('post')", cancel: true, want: run.Cancelled},
		{name: "step timeout still cleans up", main: "console.log('ready');setInterval(()=>{},1000)", post: "console.log('post')", timeout: 1, want: run.TimedOut, ranPost: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			metadata := "name: Test\ndescription: Test\nruns:\n  using: node24\n  main: main.js\n  post: post.js\n"
			if tc.postIf != "" {
				metadata += "  post-if: " + tc.postIf + "\n"
			}
			writeActionFile(t, dir, "a/action.yml", metadata)
			writeActionFile(t, dir, "a/main.js", tc.main)
			writeActionFile(t, dir, "a/post.js", tc.post)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var logs bytes.Buffer
			result, err := Execute(ctx, run.Plan{Steps: []run.Step{{ID: "action", Action: &run.Action{Path: "a"}, TimeoutSeconds: tc.timeout}}}, Options{Workspace: dir, NodeRuntimes: map[string]string{"node24": node}, Log: func(_ string, data []byte) error {
				logs.Write(data)
				if tc.cancel && bytes.Contains(data, []byte("ready")) {
					cancel()
				}
				return nil
			}})
			if err != nil || result.State != tc.want || strings.Contains(logs.String(), "post") != tc.ranPost {
				t.Fatalf("result=%+v err=%v logs=%s", result, err, logs.String())
			}
		})
	}
}

func TestActionMetadataFallbackDoesNotHideEscapingSymlink(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeActionFile(t, root, "action.yaml", "name: Test\ndescription: Test\nruns: {using: node24, main: main.js}\n")
	if _, err := loadAction(root); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "action.yml")
	writeActionFile(t, filepath.Dir(outside), "action.yml", "unsafe")
	if err := os.Symlink(outside, filepath.Join(root, "action.yml")); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAction(root); err == nil {
		t.Fatal("escaping action.yml must not fall back to action.yaml")
	}
}

func TestNodePreConditionSeesPriorJobFailure(t *testing.T) {
	node := node24(t)
	for _, condition := range []string{"success()", "failure()"} {
		t.Run(condition, func(t *testing.T) {
			dir := t.TempDir()
			writeActionFile(t, dir, "a/action.yml", "name: Test\ndescription: Test\nruns:\n  using: node24\n  pre: pre.js\n  pre-if: "+condition+"\n  main: main.js\n")
			writeActionFile(t, dir, "a/pre.js", "console.log('pre-executed')")
			writeActionFile(t, dir, "a/main.js", "console.log('main-executed')")
			var logs bytes.Buffer
			result, err := Execute(t.Context(), run.Plan{Steps: []run.Step{{ID: "fail", Run: "exit 7", Shell: "sh"}, {ID: "action", If: "always()", Action: &run.Action{Path: "a"}}}}, Options{Workspace: dir, NodeRuntimes: map[string]string{"node24": node}, Log: func(_ string, data []byte) error { logs.Write(data); return nil }})
			if err != nil || result.State != run.Failed || !strings.Contains(logs.String(), "main-executed") || strings.Contains(logs.String(), "pre-executed") != (condition == "failure()") {
				t.Fatal(result, err, logs.String())
			}
		})
	}
}
