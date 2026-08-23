package syntax

import (
	"errors"
	"strings"
	"testing"
)

const validWorkflow = `name: CI

on:
  push:
    branches:
      - main

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
      - run: |
          go build -o bin/app ./cmd/app
`

func TestParseValidWorkflow(t *testing.T) {
	wf, err := Parse("ci.yml", []byte(validWorkflow))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if wf.Name != "CI" {
		t.Errorf("name = %q", wf.Name)
	}
	if wf.On.Push == nil || len(wf.On.Push.Branches) != 1 || wf.On.Push.Branches[0] != "main" {
		t.Errorf("push trigger = %+v", wf.On.Push)
	}
	if len(wf.Jobs) != 2 {
		t.Fatalf("jobs = %d, want 2", len(wf.Jobs))
	}
	if wf.Jobs[0].ID != "test" || wf.Jobs[1].ID != "build" {
		t.Errorf("job order: %s, %s", wf.Jobs[0].ID, wf.Jobs[1].ID)
	}
	test := wf.Jobs[0]
	if test.Name != "Unit tests" || test.RunsOn != "k3s-small" || test.Env["GO"] != "1.27" {
		t.Errorf("job fields: %+v", test)
	}
	if len(test.Steps) != 2 {
		t.Fatalf("steps = %d", len(test.Steps))
	}
	if test.Steps[0].Name != "test" || test.Steps[0].Run != "go test ./..." ||
		test.Steps[0].Env["VERBOSE"] != "1" {
		t.Errorf("step 0: %+v", test.Steps[0])
	}
	if !strings.Contains(wf.Jobs[1].Steps[0].Run, "go build -o bin/app") {
		t.Errorf("literal block not preserved: %q", wf.Jobs[1].Steps[0].Run)
	}
}

func TestParsePreservesExpressionRaw(t *testing.T) {
	src := `on: push
jobs:
  a:
    runs-on: small
    env:
      BRANCH: ${{ github.ref }}
    steps:
      - run: echo ${{ env.BRANCH }}
`
	wf, err := Parse("expr.yml", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := wf.Jobs[0].Env["BRANCH"]; got != "${{ github.ref }}" {
		t.Errorf("expression rewritten: %q", got)
	}
	if got := wf.Jobs[0].Steps[0].Run; got != "echo ${{ env.BRANCH }}" {
		t.Errorf("step expression rewritten: %q", got)
	}
}

// unknownFieldFixture places `needs` at a known line/column (line 8, col 5).
const unknownJobField = `name: CI

on: push

jobs:
  test:
    runs-on: k3s-small
    timeout-minutes: 5
    steps:
      - run: echo hi
`

const unknownStepField = `on: push
jobs:
  test:
    runs-on: small
    steps:
      - name: checkout
        uses: actions/checkout@v4
      - run: echo hi
`

const unsupportedTrigger = `on:
  schedule:
    - cron: "0 9 * * 1"
jobs:
  test:
    runs-on: small
    steps:
      - run: echo hi
`

func TestParseUnknownFieldsReportLocation(t *testing.T) {
	cases := []struct {
		name     string
		src      string
		pathPart string
		field    string
		line     int
		column   int
	}{
		{"job timeout", unknownJobField, ".jobs.test", `"timeout-minutes"`, 8, 5},
		{"step uses", unknownStepField, ".steps[0]", `"uses"`, 7, 9},
		{"schedule trigger", unsupportedTrigger, ".on", `"schedule"`, 2, 3},
	}
	for _, tc := range cases {
		_, err := Parse("wf.yml", []byte(tc.src))
		if err == nil {
			t.Errorf("%s: expected error", tc.name)
			continue
		}
		var serr *Error
		if !errors.As(err, &serr) {
			t.Errorf("%s: error type %T, want *syntax.Error", tc.name, err)
			continue
		}
		if len(serr.Diagnostics) == 0 {
			t.Errorf("%s: no diagnostics", tc.name)
			continue
		}
		d := serr.Diagnostics[0]
		if d.File != "wf.yml" || d.Line != tc.line || d.Column != tc.column {
			t.Errorf("%s: location = %s:%d:%d, want wf.yml:%d:%d", tc.name, d.File, d.Line, d.Column, tc.line, tc.column)
		}
		if !strings.Contains(d.Path, tc.pathPart) {
			t.Errorf("%s: path %q lacks %q", tc.name, d.Path, tc.pathPart)
		}
		if !strings.Contains(d.Message, tc.field) || !strings.Contains(d.Message, "not in the supported subset") {
			t.Errorf("%s: message %q lacks field/subset note", tc.name, d.Message)
		}
		if !strings.Contains(err.Error(), "wf.yml:8") && tc.name == "job timeout" {
			t.Errorf("%s: error string lacks location: %v", tc.name, err)
		}
	}
}

func TestParseTypeErrors(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"steps as string", "on: push\njobs:\n  a:\n    runs-on: x\n    steps: go test\n", "expected sequence"},
		{"env int value", "on: push\njobs:\n  a:\n    runs-on: x\n    env:\n      N: 3\n    steps:\n      - run: x\n", "expected string"},
		{"on as sequence", "on:\n  - push\njobs:\n  a:\n    runs-on: x\n    steps:\n      - run: x\n", "expected `push`"},
		{"bad job id", "on: push\njobs:\n  1bad:\n    runs-on: x\n    steps:\n      - run: x\n", "job id"},
		{"invalid yaml", "\ton: push\n", "invalid YAML"},
	}
	for _, tc := range cases {
		_, err := Parse("wf.yml", []byte(tc.src))
		if err == nil {
			t.Errorf("%s: expected error", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q lacks %q", tc.name, err.Error(), tc.want)
		}
	}
}

func TestParsePushForms(t *testing.T) {
	bare := "on: push\njobs:\n  a:\n    runs-on: x\n    steps:\n      - run: x\n"
	wf, err := Parse("bare.yml", []byte(bare))
	if err != nil {
		t.Fatalf("on: push scalar: %v", err)
	}
	if wf.On.Push == nil || len(wf.On.Push.Branches) != 0 {
		t.Errorf("bare push = %+v", wf.On.Push)
	}

	withIgnore := `on:
  push:
    branches-ignore:
      - main
jobs:
  a:
    runs-on: x
    steps:
      - run: x
`
	wf, err = Parse("ign.yml", []byte(withIgnore))
	if err != nil {
		t.Fatalf("branches-ignore: %v", err)
	}
	if wf.On.Push.BranchesIgnore[0] != "main" {
		t.Errorf("branches-ignore = %+v", wf.On.Push)
	}

	if _, err := Parse("x.yml", []byte("on: pull_request\njobs:\n  a:\n    runs-on: x\n    steps:\n      - run: x\n")); err == nil {
		t.Error("pull_request must be rejected as outside the subset")
	}
}

func TestParseNoPartialASTOnError(t *testing.T) {
	_, err := Parse("wf.yml", []byte(unknownJobField))
	if err == nil {
		t.Fatal("expected error")
	}
	// Multiple problems in one document are all reported together.
	src := "on: push\njobs:\n  a:\n    runs-on: x\n    timeout-minutes: 5\n    steps:\n      - uses: u\n"
	_, err = Parse("multi.yml", []byte(src))
	var serr *Error
	if !errors.As(err, &serr) {
		t.Fatalf("error type %T", err)
	}
	if len(serr.Diagnostics) < 2 {
		t.Errorf("expected both diagnostics, got %d: %v", len(serr.Diagnostics), serr)
	}
}

func TestTrimRefPrefix(t *testing.T) {
	if got := TrimRefPrefix("refs/heads/feature/x"); got != "feature/x" {
		t.Errorf("trim = %q", got)
	}
	if got := TrimRefPrefix("refs/tags/v1"); got != "refs/tags/v1" {
		t.Errorf("non-branch ref must be untouched: %q", got)
	}
}
