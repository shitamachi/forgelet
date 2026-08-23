package compiler

import (
	"strings"
	"testing"

	"github.com/shitamachi/forgelet/internal/workflow/syntax"
)

const conditionalWorkflow = `name: Cond

on: push

jobs:
  gated:
    if: github.ref == 'refs/heads/main'
    runs-on: small
    steps:
      - name: guarded
        if: success() && github.actor != 'renovate'
        run: echo hi
        continue-on-error: true
        env:
          WHO: ${{ github.actor }}
      - run: exit 0
      - if: true
        run: echo bool-if
`

func TestCompileCarriesConditions(t *testing.T) {
	c := compileSrc(t, conditionalWorkflow)

	if len(c.Jobs) != 1 {
		t.Fatalf("jobs = %d", len(c.Jobs))
	}
	job := c.Jobs[0]
	if job.If != "github.ref == 'refs/heads/main'" {
		t.Errorf("job if = %q", job.If)
	}
	first := job.Steps[0]
	if first.If != "success() && github.actor != 'renovate'" {
		t.Errorf("step if = %q", first.If)
	}
	if !first.ContinueOnError {
		t.Error("continue-on-error lost")
	}
	if first.Env["WHO"] != "${{ github.actor }}" {
		t.Errorf("env = %v", first.Env)
	}
	if third := job.Steps[2]; third.If != "true" || third.Name != "" {
		t.Errorf("third step = %+v", third)
	}
}

func TestCompileConditionlessDefaults(t *testing.T) {
	c := compileSrc(t, validWorkflow)
	for _, j := range c.Jobs {
		if j.If != "" {
			t.Errorf("job %s unexpectedly conditioned: %q", j.Key, j.If)
		}
		for _, s := range j.Steps {
			if s.If != "" || s.ContinueOnError {
				t.Errorf("step %q unexpectedly conditional", s.Name)
			}
		}
	}
}

func TestSyntaxRejectsNonBoolContinueOnError(t *testing.T) {
	_, err := syntax.Parse("ci.yml", []byte("on: push\njobs:\n  a:\n    runs-on: x\n    steps:\n      - run: x\n        continue-on-error: sometimes\n"))
	if err == nil || !strings.Contains(err.Error(), "boolean") {
		t.Errorf("err = %v", err)
	}
}
