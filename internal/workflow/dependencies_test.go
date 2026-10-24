package workflow

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestCompileDependenciesAndConditions(t *testing.T) {
	source := `name: DAG
on: push
jobs:
  build:
    needs: []
    runs-on: linux
    if: github.ref == 'refs/heads/main'
    outputs:
      artifact: ${{ steps.package.outputs.digest }}
      outcome: ${{ steps.package.outcome }}
    steps:
      - id: package
        run: echo digest=sha256:123 >> "$GITHUB_OUTPUT"
        continue-on-error: true
      - if: ${{ failure() && steps.package.outcome == 'failure' }}
        run: echo '${{ steps.package.conclusion }}'
  test:
    needs: build
    if: needs.build.result == 'success'
    runs-on: linux
    env:
      ARTIFACT: ${{ needs.build.outputs.artifact }}
    outputs:
      copied: ${{ needs.build.outputs.artifact }}
    steps:
      - if: env.ARTIFACT != '' && needs.build.result == 'success'
        run: echo '${{ needs.build.outputs.artifact }}'
      - if: false
        run: echo never
      - if: null
        run: echo never
  deploy:
    needs: [test, build]
    if: always() && needs.test.result == 'success'
    runs-on: linux
    steps:
      - if: true
        run: echo deploy
`
	plans, err := Compile([]byte(source), testEvent, "dag")
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 3 || plans[0].Key != "build" || plans[1].Key != "deploy" || plans[2].Key != "test" {
		t.Fatalf("plan ordering/keys: %+v", plans)
	}
	build, deploy, test := plans[0], plans[1], plans[2]
	if len(build.Needs) != 0 || !reflect.DeepEqual(deploy.Needs, []string{"test", "build"}) || !reflect.DeepEqual(test.Needs, []string{"build"}) {
		t.Fatalf("dependencies: build=%v deploy=%v test=%v", build.Needs, deploy.Needs, test.Needs)
	}
	if !build.Steps[0].ContinueOnError || build.Steps[1].If != "${{ failure() && steps.package.outcome == 'failure' }}" {
		t.Fatalf("step condition/error policy lost: %+v", build.Steps)
	}
	if build.Outputs["artifact"] != "${{ steps.package.outputs.digest }}" || test.Env["ARTIFACT"] != "${{ needs.build.outputs.artifact }}" || test.If != "needs.build.result == 'success'" {
		t.Fatalf("runtime expressions altered: build=%+v test=%+v", build, test)
	}
	if test.Steps[1].If != "false" || test.Steps[2].If != "null" || deploy.Steps[0].If != "true" {
		t.Fatal("scalar conditions not retained")
	}
	if build.NeedsContext != nil || build.AncestorFailed {
		t.Fatal("compiler must not manufacture runtime dependency context")
	}
}

func TestCompileRejectsInvalidDependenciesAndConditions(t *testing.T) {
	base := "on: push\njobs:\n  first:\n    runs-on: linux\n    steps:\n      - run: echo first\n  second:\n    runs-on: linux\n%s    steps:\n%s"
	tests := []struct{ name, source, want string }{
		{"unknown", job("    needs: missing\n", "      - run: echo ok\n"), "unknown dependency"},
		{"self", job("    needs: test\n", "      - run: echo ok\n"), "depend on itself"},
		{"duplicate", fmt.Sprintf(base, "    needs: [first, first]\n", "      - run: echo ok\n"), "duplicate dependency"},
		{"cycle", strings.Replace(fmt.Sprintf(base, "    needs: first\n", "      - run: echo ok\n"), "  first:\n", "  first:\n    needs: second\n", 1), "dependency cycle"},
		{"mapping", job("    needs: {test: true}\n", "      - run: echo ok\n"), "needs must be"},
		{"boolean", job("    needs: false\n", "      - run: echo ok\n"), "needs must be"},
		{"nonstring item", job("    needs: [3]\n", "      - run: echo ok\n"), "expected a string"},
		{"dynamic", job("    needs: '${{ env.JOB }}'\n", "      - run: echo ok\n"), "static job identifiers"},
		{"too many", job("    needs: ["+strings.Repeat("first,", 65)+"]\n", "      - run: echo ok\n"), "more than 64"},
		{"job env condition", job("    if: env.FLAG\n", "      - run: echo ok\n"), "unsupported context reference"},
		{"job steps condition", job("    if: steps.first.outcome == 'success'\n", "      - run: echo ok\n"), "unsupported context reference"},
		{"undeclared need condition", fmt.Sprintf(base, "    if: needs.first.result == 'success'\n", "      - run: echo ok\n"), "unsupported context reference"},
		{"unsupported condition even dead", job("    if: false && secrets.KEY\n", "      - run: echo ok\n"), "unsupported context reference"},
		{"invalid condition", job("    if: 'success() &&'\n", "      - run: echo ok\n"), "invalid condition"},
		{"mapping condition", job("    if: {}\n", "      - run: echo ok\n"), "expected a string"},
		{"step future condition", job("", "      - if: steps.future.outcome == 'success'\n        run: echo ok\n      - id: future\n        run: echo later\n"), "unsupported context reference"},
		{"step self condition", job("", "      - id: myself\n        if: steps.myself.outcome == 'success'\n        run: echo ok\n"), "unsupported context reference"},
		{"step string coe", job("", "      - run: echo ok\n        continue-on-error: 'true'\n"), "literal boolean"},
		{"step unknown coe", job("", "      - run: echo ok\n        continue-on-error: '${{ unknown.FLAG }}'\n"), "unsupported context"},
		{"output unknown step", job("    outputs: {value: '${{ steps.missing.outputs.a }}'}\n", "      - run: echo ok\n"), "unsupported expression"},
		{"output implicit step", job("    outputs: {value: '${{ steps.__step_1.outputs.a }}'}\n", "      - run: echo ok\n"), "unsupported expression"},
		{"output env", job("    outputs: {value: '${{ env.FLAG }}'}\n", "      - run: echo ok\n"), "unsupported expression"},
		{"output bad key", job("    outputs: {'bad key': x}\n", "      - run: echo ok\n"), "invalid output identifier"},
		{"output map value", job("    outputs: {value: {}}\n", "      - run: echo ok\n"), "expected a string"},
		{"output not map", job("    outputs: []\n", "      - run: echo ok\n"), "expected a mapping"},
		{"output size", job("    outputs: {value: '"+strings.Repeat("a", 64<<10)+"'}\n", "      - run: echo ok\n"), "exceed 64 KiB"},
		{"unknown need interpolation", job("", "      - run: echo '${{ needs.other.result }}'\n"), "unsupported expression"},
		{"bad step state", job("", "      - id: previous\n        run: echo ok\n      - run: echo '${{ steps.previous.result }}'\n"), "unsupported expression"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compile([]byte(tc.source), testEvent, "run")
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "line ") {
				t.Fatalf("got %v, want located %q", err, tc.want)
			}
		})
	}
}

func TestDependencyContextIsDirectOnly(t *testing.T) {
	source := `on: push
jobs:
  first:
    runs-on: linux
    steps: [{run: echo first}]
  second:
    needs: first
    runs-on: linux
    steps: [{run: echo second}]
  third:
    needs: second
    if: needs.first.result == 'success'
    runs-on: linux
    steps: [{run: echo third}]
`
	_, err := Compile([]byte(source), testEvent, "run")
	if err == nil || !strings.Contains(err.Error(), "unsupported context reference") {
		t.Fatal(err)
	}
}

func TestJobOutputsCountLimit(t *testing.T) {
	var fields strings.Builder
	fields.WriteString("    outputs:\n")
	for i := range 101 {
		fmt.Fprintf(&fields, "      output%d: value\n", i)
	}
	_, err := Compile([]byte(job(fields.String(), "      - run: echo ok\n")), testEvent, "run")
	if err == nil || !strings.Contains(err.Error(), "more than 100") {
		t.Fatal(err)
	}
}
