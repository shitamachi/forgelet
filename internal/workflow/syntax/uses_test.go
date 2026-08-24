package syntax

import (
	"strings"
	"testing"
)

const usesWorkflow = `name: Uses

on: push

jobs:
  ci:
    runs-on: small
    steps:
      - name: checkout
        uses: actions/checkout@v4
        with:
          fetch-depth: "1"
          token: ${{ secrets.PAT }}
      - run: go test ./...
`

func TestParseUsesStep(t *testing.T) {
	wf, err := Parse("uses.yml", []byte(usesWorkflow))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	step := wf.Jobs[0].Steps[0]
	if step.Uses != "actions/checkout@v4" || step.Run != "" {
		t.Errorf("step = uses:%q run:%q", step.Uses, step.Run)
	}
	if step.With["fetch-depth"] != "1" || step.With["token"] != "${{ secrets.PAT }}" {
		t.Errorf("with = %v", step.With)
	}
}

func TestParseUsesErrors(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			"run and uses together",
			"on: push\njobs:\n  a:\n    runs-on: x\n    steps:\n      - run: x\n        uses: actions/checkout@v4\n",
			"mutually exclusive",
		},
		{
			"neither run nor uses",
			"on: push\njobs:\n  a:\n    runs-on: x\n    steps:\n      - name: empty\n",
			`expected "run" or "uses"`,
		},
	}
	for _, tc := range cases {
		_, err := Parse("x.yml", []byte(tc.src))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want substring %q", tc.name, err, tc.want)
		}
	}
}

// A uses step may still carry if/env/continue-on-error like a run step.
func TestParseUsesMixedFields(t *testing.T) {
	src := `on: push
jobs:
  a:
    runs-on: x
    steps:
      - uses: actions/cache@v4
        if: runner.os == 'Linux'
        continue-on-error: true
        env:
          EXTRA: "1"
`
	wf, err := Parse("x.yml", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	s := wf.Jobs[0].Steps[0]
	if s.If == "" || !s.ContinueOnError || s.Env["EXTRA"] != "1" {
		t.Errorf("mixed fields lost: %+v", s)
	}
}
