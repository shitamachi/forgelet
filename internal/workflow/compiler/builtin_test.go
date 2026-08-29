package compiler

import (
	"strings"
	"testing"

	"github.com/shitamachi/forgelet/internal/workflow/syntax"
)

func TestBuiltinRegistryResolve(t *testing.T) {
	src := `name: CI
on: push
jobs:
  test:
    runs-on: small
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: "1"
      - uses: actions/cache@v4
        with:
          key: mykey
          path: /tmp/p
`
	wf, err := syntax.Parse("ci.yml", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	c, err := Compile(wf)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(c.Jobs[0].Steps) != 2 {
		t.Fatalf("steps = %d", len(c.Jobs[0].Steps))
	}
	if c.Jobs[0].Steps[0].Uses.Action != "actions/checkout" || c.Jobs[0].Steps[0].Uses.Version != "v4" {
		t.Errorf("checkout = %+v", c.Jobs[0].Steps[0].Uses)
	}
	if c.Jobs[0].Steps[1].Uses.Action != "actions/cache" {
		t.Errorf("cache = %+v", c.Jobs[0].Steps[1].Uses)
	}
}

func TestBuiltinUnknownAction(t *testing.T) {
	// Unknown non-builtin is now treated as generic JS/composite (0012),
	// not as a builtin error. Only close typos of builtins still hint.
	src := `name: CI
on: push
jobs:
  test:
    runs-on: small
    steps:
      - uses: evil/hack@v1
`
	wf, err := syntax.Parse("ci.yml", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	c, err := Compile(wf)
	if err != nil {
		t.Fatalf("compile should handle generic uses, got %v", err)
	}
	if c.Jobs[0].Steps[0].RawUses != "evil/hack@v1" {
		t.Errorf("RawUses = %q, want evil/hack@v1", c.Jobs[0].Steps[0].RawUses)
	}
	// Close typo of a builtin still hints
	src2 := `name: CI
on: push
jobs:
  test:
    runs-on: small
    steps:
      - uses: actions/checkaut@v4
`
	wf2, _ := syntax.Parse("ci.yml", []byte(src2))
	_, err2 := Compile(wf2)
	if err2 == nil || !strings.Contains(err2.Error(), "did you mean") {
		t.Fatalf("expected hint, got %v", err2)
	}
}

func TestBuiltinUnknownInputWarning(t *testing.T) {
	src := `name: CI
on: push
jobs:
  test:
    runs-on: small
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: "1"
          unknown-key: "oops"
`
	wf, _ := syntax.Parse("ci.yml", []byte(src))
	c, err := Compile(wf)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0].Msg, "unknown-key") {
		t.Fatalf("warnings = %+v", c.Warnings)
	}
	if _, ok := c.Jobs[0].Steps[0].Uses.Inputs["unknown-key"]; ok {
		t.Error("unknown input should not be in compiled inputs")
	}
}

func TestBuiltinUsesAndRunConflictAtSyntax(t *testing.T) {
	src := `name: CI
on: push
jobs:
  test:
    runs-on: small
    steps:
      - uses: actions/checkout@v4
        run: echo hi
`
	_, err := syntax.Parse("ci.yml", []byte(src))
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutually exclusive, got %v", err)
	}
}
