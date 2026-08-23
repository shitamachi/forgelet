package compiler

import (
	"strings"
	"testing"

	"github.com/shitamachi/forgelet/internal/workflow/syntax"
)

// compileExpectErr compiles and returns the error (nil when it compiles).
func compileExpectErr(t *testing.T, src string) error {
	t.Helper()
	wf, perr := syntax.Parse("wf.yml", []byte(src))
	if perr != nil {
		return perr
	}
	_, cerr := Compile(wf)
	return cerr
}

const matrixWorkflow = `on: push
jobs:
  test:
    runs-on: k3s-small
    strategy:
      matrix:
        go: ["1.26", "1.27"]
        os: [linux, darwin]
    steps:
      - run: go test
`

func TestMatrixExpansionStableKeys(t *testing.T) {
	c := mustCompile(t, matrixWorkflow)
	if len(c.Jobs) != 4 {
		t.Fatalf("instances = %d, want 4", len(c.Jobs))
	}
	// Axes sorted by name in the key; values keep document order.
	wantKeys := []string{
		"test[1.26,linux]",
		"test[1.26,darwin]",
		"test[1.27,linux]",
		"test[1.27,darwin]",
	}
	for i, want := range wantKeys {
		if c.Jobs[i].Key != want {
			t.Errorf("instance %d key = %q, want %q (axes sorted, first axis outer)", i, c.Jobs[i].Key, want)
		}
		if !strings.HasPrefix(c.Jobs[i].DisplayName, "test (") {
			t.Errorf("display name = %q", c.Jobs[i].DisplayName)
		}
		if c.Jobs[i].Matrix["go"] == "" || c.Jobs[i].Matrix["os"] == "" {
			t.Errorf("matrix values missing: %+v", c.Jobs[i].Matrix)
		}
	}

	// Deterministic across recompiles (retry stability, FR-2.5).
	again := mustCompile(t, matrixWorkflow)
	for i := range c.Jobs {
		if c.Jobs[i].Key != again.Jobs[i].Key {
			t.Fatalf("expansion order unstable at %d", i)
		}
	}

	// Intents carry the matrix combination.
	intents := c.JobIntents()
	if len(intents) != 4 || intents[0].Matrix["go"] != "1.26" || intents[0].Matrix["os"] != "linux" {
		t.Errorf("intents = %+v", intents[:1])
	}
}

func TestMatrixValidation(t *testing.T) {
	empty := `on: push
jobs:
  a:
    runs-on: x
    strategy:
      matrix:
        go: []
    steps:
      - run: x
`
	if err := compileExpectErr(t, empty); err == nil || !strings.Contains(err.Error(), "no values") {
		t.Fatalf("empty axis: %v", err)
	}

	include := `on: push
jobs:
  a:
    runs-on: x
    strategy:
      matrix:
        go: ["1"]
      include:
        - go: "2"
    steps:
      - run: x
`
	if err := compileExpectErr(t, include); err == nil || !strings.Contains(err.Error(), "not in the supported subset") {
		t.Fatalf("include must be out-of-subset: %v", err)
	}
}

const needsWorkflow = `on: push
jobs:
  build:
    runs-on: k3s-build
    steps:
      - run: build
  test:
    needs: build
    runs-on: k3s-small
    steps:
      - run: test
  deploy:
    needs: [build, test]
    runs-on: k3s-deploy
    steps:
      - run: deploy
`

func TestNeedsTopologicalOrder(t *testing.T) {
	c := mustCompile(t, needsWorkflow)
	if len(c.Jobs) != 3 {
		t.Fatalf("jobs = %d", len(c.Jobs))
	}
	if c.Jobs[0].Key != "build" || c.Jobs[1].Key != "test" || c.Jobs[2].Key != "deploy" {
		t.Errorf("order = %s, %s, %s", c.Jobs[0].Key, c.Jobs[1].Key, c.Jobs[2].Key)
	}
	if len(c.Jobs[2].DependsOn) != 2 {
		t.Errorf("deploy deps = %v", c.Jobs[2].DependsOn)
	}
	// Document order with deps later in the file still works.
	reordered := `on: push
jobs:
  deploy:
    needs: test
    runs-on: x
    steps:
      - run: d
  test:
    runs-on: x
    steps:
      - run: t
`
	c2 := mustCompile(t, reordered)
	if c2.Jobs[0].Key != "test" || c2.Jobs[1].Key != "deploy" {
		t.Errorf("topo order = %s, %s", c2.Jobs[0].Key, c2.Jobs[1].Key)
	}
}

func TestNeedsValidation(t *testing.T) {
	unknown := `on: push
jobs:
  a:
    needs: ghost
    runs-on: x
    steps:
      - run: x
`
	if err := compileExpectErr(t, unknown); err == nil || !strings.Contains(err.Error(), "unknown job") {
		t.Fatalf("unknown dep: %v", err)
	}
	cycle := `on: push
jobs:
  a:
    needs: b
    runs-on: x
    steps:
      - run: x
  b:
    needs: a
    runs-on: x
    steps:
      - run: x
`
	if err := compileExpectErr(t, cycle); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle: %v", err)
	}
	self := `on: push
jobs:
  a:
    needs: a
    runs-on: x
    steps:
      - run: x
`
	if err := compileExpectErr(t, self); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("self cycle: %v", err)
	}
}

func TestMatrixAndNeedsIntents(t *testing.T) {
	src := `on: push
jobs:
  build:
    runs-on: b
    steps:
      - run: x
  test:
    needs: build
    runs-on: t
    strategy:
      matrix:
        arch: [amd64, arm64]
    steps:
      - run: x
`
	c := mustCompile(t, src)
	intents := c.JobIntents()
	if len(intents) != 3 {
		t.Fatalf("intents = %d", len(intents))
	}
	for _, intent := range intents[1:] {
		if len(intent.DependsOn) != 1 || intent.DependsOn[0] != "build" {
			t.Errorf("matrix instance lost deps: %+v", intent)
		}
		if intent.Matrix["arch"] == "" {
			t.Errorf("matrix instance lost matrix: %+v", intent)
		}
	}
}
