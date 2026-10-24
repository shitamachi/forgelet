package workflow

import (
	"errors"
	"strings"
	"testing"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestTriggerBranchAndPathFilters(t *testing.T) {
	source := `on:
  push:
    branches: [main, 'release/**', '!release/private/**']
    paths: ['**/*.go', '!vendor/**']
jobs:
  test:
    runs-on: linux
    steps: [{run: 'echo ok'}]
`
	for _, tc := range []struct {
		ref         string
		files       []string
		known, want bool
	}{
		{"refs/heads/main", []string{"main.go"}, true, true}, {"refs/heads/release/v2", []string{"src/main.go"}, true, true},
		{"refs/heads/release/private/v2", []string{"main.go"}, true, false}, {"refs/heads/main", []string{"vendor/main.go"}, true, false},
		{"refs/tags/main", []string{"main.go"}, true, false}, {"refs/heads/main", nil, false, false},
	} {
		event := testEvent
		event.Ref, event.Files, event.FilesKnown = tc.ref, tc.files, tc.known
		plans, err := Compile([]byte(source), event, "filtered")
		if tc.want {
			if err != nil || len(plans) != 1 {
				t.Fatal(plans, err)
			}
		} else if !errors.Is(err, ErrNotTriggered) && !errors.Is(err, ErrFilesRequired) {
			t.Fatal(err)
		}
	}
}
func TestDispatchTypedInputsAndDefaults(t *testing.T) {
	source := `on:
  workflow_dispatch:
    inputs:
      enabled: {type: boolean, default: false}
      count: {type: number, default: 2}
      target: {type: choice, options: [dev, prod], required: true}
jobs:
  test:
    runs-on: ${{ inputs.target }}
    if: inputs.enabled == true
    steps: [{run: 'echo ${{ inputs.count }}'}]
`
	p, err := Compile([]byte(source), run.Event{Name: "workflow_dispatch", Inputs: map[string]any{"target": "dev", "enabled": true}}, "dispatch")
	if err != nil || p[0].Profile != "dev" || p[0].Inputs["count"] != float64(2) {
		t.Fatal(p, err)
	}
	for _, inputs := range []map[string]any{{}, {"target": "unknown"}, {"target": "dev", "enabled": "true"}, {"target": "dev", "extra": "x"}} {
		if _, err := Compile([]byte(source), run.Event{Name: "workflow_dispatch", Inputs: inputs}, "bad"); err == nil {
			t.Error("accepted", inputs)
		}
	}
}
func TestSecretsAreReferencesAndForbiddenInConditions(t *testing.T) {
	source := job("    env: {TOKEN: '${{ secrets.API_KEY }}'}\n", "      - run: echo '${{ secrets.OTHER }} ${{ env.TOKEN }}'\n")
	p, err := Compile([]byte(source), testEvent, "secret")
	if err != nil || strings.Join(p[0].SecretNames, ",") != "API_KEY,OTHER" {
		t.Fatal(p, err)
	}
	if _, err := Compile([]byte(job("    if: secrets.API_KEY != ''\n", "      - run: echo ok\n")), testEvent, "bad"); err == nil {
		t.Fatal("accepted secret in scheduler condition")
	}
}

func TestPullRequestDefaultAndExplicitActivityTypes(t *testing.T) {
	for _, tc := range []struct {
		on, action string
		want       bool
	}{
		{"pull_request", "opened", true},
		{"[pull_request]", "reopened", true},
		{"{pull_request: {}}", "synchronize", true},
		{"pull_request", "ready_for_review", false},
		{"{pull_request: {}}", "ready_for_review", false},
		{"{pull_request: {types: [ready_for_review]}}", "ready_for_review", true},
		{"{pull_request: {types: [ready_for_review]}}", "opened", false},
	} {
		t.Run(tc.on+"/"+tc.action, func(t *testing.T) {
			source := "on: " + tc.on + "\njobs:\n  test:\n    runs-on: linux\n    steps: [{run: 'echo ok'}]\n"
			_, err := Compile([]byte(source), run.Event{Name: "pull_request", Action: tc.action, BaseRef: "main"}, "pr")
			if tc.want && err != nil || !tc.want && !errors.Is(err, ErrNotTriggered) {
				t.Fatal(err)
			}
		})
	}
}
