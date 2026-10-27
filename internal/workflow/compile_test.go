package workflow

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shitamachi/forgelet/internal/action"
	"github.com/shitamachi/forgelet/internal/run"
)

var testEvent = run.Event{Repository: "owner/repo", SHA: strings.Repeat("a", 40), Ref: "refs/heads/main"}

func TestCompileIndependentJobs(t *testing.T) {
	source := []byte(`name: CI
on: [push]
env:
  GLOBAL: hello
  OVERRIDE: workflow
jobs:
  zeta:
    runs-on: linux
    steps:
      - run: echo zeta
  alpha:
    name: Go test
    runs-on: linux-arm64
    timeout-minutes: 2
    env:
      OVERRIDE: job
      SHA: ${{ github.sha }}
    steps:
      - uses: actions/checkout@v4
        with:
          path: src
          fetch-depth: 0
      - id: build
        name: Build
        run: echo "answer=42" >> "$GITHUB_OUTPUT"
        shell: sh
        timeout-minutes: 1
        working-directory: src
        env:
          LOCAL: ${{ env.GLOBAL }}
          NUMBER: 2
          BOOL: true
      - run: echo '${{ steps.build.outputs.answer }} ${{ github.ref }} ${{ env.OVERRIDE }}'
`)
	plans, err := Compile(source, testEvent, "run1")
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 2 || plans[0].Name != "CI / Go test" || plans[1].Name != "CI / zeta" {
		t.Fatalf("unexpected plans: %+v", plans)
	}
	plan := plans[0]
	if plan.Profile != "linux-arm64" || plan.TimeoutSeconds != 120 || plan.Env["OVERRIDE"] != "job" || plan.Env["GLOBAL"] != "hello" || plan.Env["SHA"] != "${{ github.sha }}" {
		t.Fatalf("incorrect job properties: %+v", plan)
	}
	if plan.Repository != testEvent.Repository || plan.SHA != testEvent.SHA || plan.Ref != testEvent.Ref {
		t.Fatal("event context not retained")
	}
	if plan.Steps[0].Checkout.Path != "src" || plan.Steps[0].Checkout.FetchDepth != 0 || plan.Steps[1].TimeoutSeconds != 60 || plan.Steps[1].Env["BOOL"] != "true" {
		t.Fatalf("incorrect steps: %+v", plan.Steps)
	}
	if len(plan.JobID) != 24 || plans[1].TimeoutSeconds != 1200 || plans[1].Steps[0].Shell != "bash" {
		t.Fatal("incorrect defaults")
	}
	again, err := Compile(source, testEvent, "run1")
	if err != nil || again[0].JobID != plan.JobID {
		t.Fatal("job ID is not deterministic")
	}
	newRun, err := Compile(source, testEvent, "run2")
	if err != nil || newRun[0].JobID == plan.JobID || newRun[0].JobID == newRun[1].JobID {
		t.Fatal("job identities collide")
	}
}

func TestPushTriggerForms(t *testing.T) {
	for _, trigger := range []string{"push", "[push]", "{push: null}", "{push: {}}"} {
		t.Run(trigger, func(t *testing.T) {
			plans, err := Compile([]byte("on: "+trigger+"\njobs:\n  test:\n    runs-on: linux\n    steps:\n      - uses: actions/checkout@v4\n"), testEvent, "run")
			if err != nil {
				t.Fatal(err)
			}
			if plans[0].Steps[0].Checkout.FetchDepth != 1 || plans[0].Name != "test" {
				t.Fatal("incorrect checkout default or name")
			}
		})
	}
}

func TestCompileRejectsUnsupportedAndInvalidWorkflows(t *testing.T) {
	tests := []struct {
		name, document, want string
	}{
		{"unknown top-level", "on: push\npermissions: write-all\njobs: {}", `execution capability permissions`},
		{"job continue on error", job("    continue-on-error: yes\n", "      - run: echo ok\n"), "expected boolean"},
		{"matrix", job("    strategy: {matrix: {go: []}}\n", "      - run: true\n"), "nonempty scalar array"},
		{"duplicate", "on: push\non: push\njobs: {}", "duplicate field"},
		{"unknown step", job("", "      - run: echo ok\n        retry: 3\n"), `unsupported field "retry"`},
		{"duplicate env", job("    env: {A: x, A: y}\n", "      - run: echo ok\n"), "duplicate field"},
		{"bad env key", job("    env: {'a-b': x}\n", "      - run: echo ok\n"), "invalid or reserved"},
		{"reserved env", job("    env: {GITHUB_TOKEN: x}\n", "      - run: echo ok\n"), "invalid or reserved"},
		{"null env", job("    env: {A: null}\n", "      - run: echo ok\n"), "expected a string"},
		{"same env map", job("", "      - run: echo ok\n        env: {A: '${{ env.A }}'}\n"), "same env mapping"},
		{"workflow env dependency", "on: push\nenv: {A: '${{ env.B }}'}\njobs: {}", "unsupported expression"},
		{"no on", "jobs: {}", "requires on: push"},
		{"pull request", "on: pull_request\njobs: {}", "does not match event"},
		{"mixed trigger empty jobs", "on: [push, pull_request]\njobs: {}", "between 1 and 64"},
		{"branch filter empty jobs", "on: {push: {branches: [main]}}\njobs: {}", "between 1 and 64"},
		{"unknown trigger", "on: {schedule: []}\njobs: {}", `unsupported field "schedule"`},
		{"empty trigger", "on: {}\njobs: {}", "at least one supported event"},
		{"no jobs", "on: push", "expected a mapping"},
		{"empty jobs", "on: push\njobs: {}", "between 1 and 64"},
		{"bad job key", "on: push\njobs: {'bad key': {}}", "invalid job identifier"},
		{"dynamic profile", strings.Replace(job("", "      - run: echo ok\n"), "runs-on: linux", "runs-on: '${{ env.PROFILE }}'", 1), "nonempty profile"},
		{"profile labels", strings.Replace(job("", "      - run: echo ok\n"), "runs-on: linux", "runs-on: [linux]", 1), "expected a string"},
		{"bad profile", strings.Replace(job("", "      - run: echo ok\n"), "runs-on: linux", "runs-on: ''", 1), "nonempty profile"},
		{"bad workflow name", "name: '${{ github.ref }}'\n" + job("", "      - run: echo ok\n"), "workflow name must be static"},
		{"bad job name", job("    name: '${{ env.NAME }}'\n", "      - run: echo ok\n"), "invalid early expression"},
		{"bad step name", job("", "      - name: '${{ github.ref }}'\n        run: echo ok\n"), "step name must be static"},
		{"empty steps", job("", ""), "nonempty sequence"},
		{"both step types", job("", "      - run: echo ok\n        uses: actions/checkout@v4\n"), "exactly one"},
		{"neither step type", job("", "      - name: Empty\n"), "exactly one"},
		{"empty run", job("", "      - run: ''\n"), "nonempty string"},
		{"run with", job("", "      - run: echo ok\n        with: {}\n"), "only supported for builtin"},
		{"unknown shell", job("", "      - run: echo ok\n        shell: python\n"), "supported shells"},
		{"shell mapping", job("", "      - run: echo ok\n        shell: {}\n"), "expected a string"},
		{"directory escape", job("", "      - run: echo ok\n        working-directory: ../escape\n"), "inside the workspace"},
		{"directory absolute", job("", "      - run: echo ok\n        working-directory: /tmp\n"), "inside the workspace"},
		{"step ID collision", job("", "      - id: test\n        run: echo one\n      - id: test\n        run: echo two\n"), "duplicate step ID"},
		{"step ID reserved", job("", "      - id: __step_1\n        run: echo ok\n"), "reserved step ID"},
		{"step ID invalid", job("", "      - id: 'bad id'\n        run: echo ok\n"), "invalid or reserved"},
		{"missing action ref", job("", "      - uses: actions/setup-go\n"), "invalid public Action reference"},
		{"invalid action ref", job("", "      - uses: actions/checkout@../main\n"), "invalid public Action reference"},
		{"checkout path escape", job("", "      - uses: actions/checkout@v4\n        with: {path: ../escape}\n"), "inside the workspace"},
		{"checkout dynamic path", job("", "      - uses: actions/checkout@v4\n        with: {path: '${{ env.PATH }}'}\n"), "static path"},
		{"checkout bad depth", job("", "      - uses: actions/checkout@v4\n        with: {fetch-depth: -1}\n"), "nonnegative integer"},
		{"checkout bad depth type", job("", "      - uses: actions/checkout@v4\n        with: {fetch-depth: {}}\n"), "expected a string"},
		{"checkout shell", job("", "      - uses: actions/checkout@v4\n        shell: bash\n"), "only supported for run"},
		{"timeout zero", job("    timeout-minutes: 0\n", "      - run: echo ok\n"), "integer from 1"},
		{"timeout excessive", job("", "      - run: echo ok\n        timeout-minutes: 1441\n"), "integer from 1"},
		{"timeout invalid", job("    timeout-minutes: 1.5\n", "      - run: echo ok\n"), "integer from 1"},
		{"unknown context", job("", "      - run: echo '${{ unknown.TOKEN }}'\n"), "unsupported expression"},

		{"unclosed expression", job("", "      - run: echo '${{ env.A'\n"), "unterminated expression"},
		{"future output", job("", "      - run: echo '${{ steps.later.outputs.a }}'\n      - id: later\n        run: echo ok\n"), "unsupported expression"},
		{"anchor", "on: push\njobs: &jobs {}", "anchors and aliases"},
		{"two documents", job("", "      - run: echo ok\n") + "---\non: push\njobs: {}", "exactly one YAML"},
		{"nonstring map key", "on: push\njobs: {1: {}}", "mapping keys must be strings"},
		{"malformed yaml", "jobs: [", "decode workflow"},
		{"root sequence", "- on: push", "expected a mapping"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compile([]byte(tc.document), testEvent, "run")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v; want error containing %q", err, tc.want)
			}
		})
	}
}

func TestCompileLimitsAndLocation(t *testing.T) {
	_, err := Compile(bytes.Repeat([]byte("x"), maxSourceBytes+1), testEvent, "run")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatal(err)
	}
	_, err = Compile([]byte("on: push"), testEvent, "")
	if err == nil || !strings.Contains(err.Error(), "run ID") {
		t.Fatal(err)
	}
	_, err = Compile([]byte("on: push\npermissions: write-all"), testEvent, "run")
	if err == nil || !strings.Contains(err.Error(), "line 2, column 1") {
		t.Fatal(err)
	}
	_, err = Compile([]byte("on: push\njobs: "+strings.Repeat("[", 40)+strings.Repeat("]", 40)), testEvent, "run")
	if err == nil || !strings.Contains(err.Error(), "too complex") {
		t.Fatal(err)
	}
}

func job(fields, steps string) string {
	return "on: push\njobs:\n  test:\n    runs-on: linux\n" + fields + "    steps:\n" + steps
}

func TestValidatedSetupActionInputs(t *testing.T) {
	for _, tc := range []struct {
		uses, inputs string
		want         bool
	}{
		{"actions/setup-go@v6.5.0", "{go-version: '1.25.0', cache: false}", true},
		{"actions/setup-go@v6.5.0", "{go-version: '1.25.0', cache: true}", false},
		{"actions/setup-go@v6.5.0", "{go-version-file: go.mod, cache: false}", false},
		{"actions/setup-go@v6.5.0", "{go-version: '1.25.0', cache: false, token: unexpected}", false},
		{"actions/setup-go@v6.5.0", "{cache: false}", false},
		{"actions/setup-node@v6.5.0", "{node-version: '24.14.0', package-manager-cache: false}", true},
		{"actions/setup-node@v6.5.0", "{node-version: '24.14.0', package-manager-cache: false, cache: npm}", false},
		{"actions/setup-node@v6.5.0", "{node-version: '24.14.0'}", false},
		{"actions/setup-node@v6.5.0", "{node-version: '24.14.0', package-manager-cache: false, registry-url: 'https://example.test'}", false},
	} {
		t.Run(tc.uses+tc.inputs, func(t *testing.T) {
			plans, err := Compile([]byte(job("", "      - uses: "+tc.uses+"\n        with: "+tc.inputs+"\n")), testEvent, "setup")
			if err != nil {
				t.Fatalf("standard workflow was rejected before profile admission: %v", err)
			}
			err = action.ValidateNative(plans[0].Steps[0].Action)
			if tc.want {
				if err != nil || plans[0].Steps[0].Action.Inputs["token"] != "" {
					t.Fatal(plans, err)
				}
			} else if err == nil {
				t.Fatal("accepted unsupported setup inputs")
			}
		})
	}
}
