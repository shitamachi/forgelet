package compiler

import (
	"strings"
	"testing"

	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/workflow/syntax"
)

const validWorkflow = `name: CI

on:
  push:
    branches:
      - main
      - "releases/*"

jobs:
  test:
    name: Unit tests
    runs-on: k3s-small
    env:
      GO: "1.27"
    steps:
      - name: test
        run: go test ./...
        env:
          VERBOSE: "1"
      - run: go build ./...

  build:
    runs-on: k3s-medium
    steps:
      - run: go build ./cmd/app
`

func compileSrc(t *testing.T, src string) *Compiled {
	t.Helper()
	wf, err := syntax.Parse("ci.yml", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	c, err := Compile(wf)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return c
}

func TestCompilePreservesOrderAndIR(t *testing.T) {
	c := compileSrc(t, validWorkflow)

	if c.Name != "CI" || len(c.Jobs) != 2 {
		t.Fatalf("compiled = %+v", c)
	}
	test := c.Jobs[0]
	if test.Key != "test" || test.DisplayName != "Unit tests" || test.RunnerClass != "k3s-small" {
		t.Errorf("job instance: %+v", test)
	}
	if test.Env["GO"] != "1.27" {
		t.Errorf("job env: %v", test.Env)
	}
	if len(test.Steps) != 2 ||
		test.Steps[0].Name != "test" || test.Steps[0].Run != "go test ./..." ||
		test.Steps[0].Env["VERBOSE"] != "1" ||
		test.Steps[1].Name != "" {
		t.Errorf("steps: %+v", test.Steps)
	}
	// Default display name falls back to the job id.
	if c.Jobs[1].DisplayName != "build" {
		t.Errorf("display name = %q", c.Jobs[1].DisplayName)
	}
}

func TestCompileSemanticErrors(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"no jobs", "on: push\njobs: {}\n", "no jobs"},
		{"empty runs-on", "on: push\njobs:\n  a:\n    runs-on: \"\"\n    steps:\n      - run: x\n", "empty runs-on"},
		{"no steps", "on: push\njobs:\n  a:\n    runs-on: x\n    steps: []\n", "no steps"},
		{"empty run", "on: push\njobs:\n  a:\n    runs-on: x\n    steps:\n      - run: \"\"\n", "empty run"},
	}
	for _, tc := range cases {
		wf, err := syntax.Parse("wf.yml", []byte(tc.src))
		if err != nil {
			t.Fatalf("%s: parse: %v", tc.name, err)
		}
		_, cerr := Compile(wf)
		if cerr == nil || !strings.Contains(cerr.Error(), tc.want) {
			t.Errorf("%s: error %v lacks %q", tc.name, cerr, tc.want)
		}
	}
	if _, err := Compile(nil); err == nil {
		t.Error("nil workflow must fail")
	}
}

func TestJobIntentsBridge(t *testing.T) {
	c := compileSrc(t, validWorkflow)
	intents := c.JobIntents()
	if len(intents) != 2 {
		t.Fatalf("intents = %d", len(intents))
	}
	want := model.JobIntent{JobKey: "test", RunnerClass: "k3s-small"}
	if intents[0] != want {
		t.Errorf("intent 0 = %+v, want %+v", intents[0], want)
	}
	// The intents must satisfy the scheduler Compiler contract (validated by
	// model.JobIntent.Validate).
	for _, intent := range intents {
		if err := intent.Validate(); err != nil {
			t.Errorf("intent %+v invalid: %v", intent, err)
		}
	}
}

func TestMatchesPushMatrix(t *testing.T) {
	cases := []struct {
		name    string
		trigger string
		ref     string
		want    bool
	}{
		{"bare push matches anything", "on: push\n", "refs/heads/anything", true},
		{"branches include match", "on:\n  push:\n    branches:\n      - main\n", "refs/heads/main", true},
		{"branches include miss", "on:\n  push:\n    branches:\n      - main\n", "refs/heads/dev", false},
		{"glob star-slash", "on:\n  push:\n    branches:\n      - \"releases/*\"\n", "refs/heads/releases/1.2", true},
		{"glob miss", "on:\n  push:\n    branches:\n      - \"releases/*\"\n", "refs/heads/main", false},
		{"ignore wins", "on:\n  push:\n    branches-ignore:\n      - main\n", "refs/heads/main", false},
		{"ignore passes others", "on:\n  push:\n    branches-ignore:\n      - main\n", "refs/heads/dev", true},
		{"exclusion prefix wins", "on:\n  push:\n    branches:\n      - \"releases/*\"\n      - \"!releases/wip\"\n", "refs/heads/releases/wip", false},
		{"exclusion prefix others pass", "on:\n  push:\n    branches:\n      - \"releases/*\"\n      - \"!releases/wip\"\n", "refs/heads/releases/1.0", true},
	}
	for _, tc := range cases {
		src := tc.trigger + "jobs:\n  a:\n    runs-on: x\n    steps:\n      - run: echo\n"
		c := compileSrc(t, src)
		if got := c.MatchesPush(tc.ref); got != tc.want {
			t.Errorf("%s: MatchesPush(%s) = %v, want %v", tc.name, tc.ref, got, tc.want)
		}
	}

	// A workflow without a push trigger never matches push events.
	noPush := compileSrc(t, "on:\n  push:\n    branches-ignore:\n      - \"*\"\njobs:\n  a:\n    runs-on: x\n    steps:\n      - run: x\n")
	if noPush.MatchesPush("refs/heads/main") {
		t.Error("excluded branch matched")
	}
}
