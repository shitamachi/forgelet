package workflow

import (
	"reflect"
	"strings"
	"testing"
)

func TestMatrixExpansionAndEarlyExpressions(t *testing.T) {
	source := `on: push
concurrency: {group: 'ci-${{ github.ref }}', cancel-in-progress: true}
jobs:
  test:
    name: Test ${{ matrix.os }} ${{ matrix.version }}
    runs-on: ${{ matrix.os }}
    continue-on-error: ${{ matrix.experimental == true }}
    concurrency: test-${{ matrix.os }}-${{ matrix.version }}
    strategy:
      max-parallel: 2
      fail-fast: false
      matrix:
        os: [arm, amd]
        version: ['1', '2']
        exclude: [{os: amd, version: '1'}]
        include: [{os: arm, experimental: true}, {os: extra, version: '3'}]
    steps:
      - run: echo '${{ format('{0}-{1}', matrix.os, matrix.version) }}'
  join:
    needs: test
    runs-on: arm
    steps: [{run: 'echo done'}]
`
	plans, err := Compile([]byte(source), testEvent, "matrix")
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 5 {
		t.Fatalf("plans=%+v", plans)
	}
	seen := map[string]bool{}
	for _, p := range plans {
		if p.WorkflowConcurrency != "ci-refs/heads/main" || !p.WorkflowCancelInProgress {
			t.Fatal(p)
		}
		if seen[p.JobID] {
			t.Fatal("duplicate ID")
		}
		seen[p.JobID] = true
		if p.Group == "test" {
			if p.MaxParallel != 2 || p.FailFast || p.MatrixTotal != 4 || p.Profile != p.Matrix["os"] || p.ContinueOnError != (p.Matrix["os"] == "arm") {
				t.Fatal(p)
			}
		}
	}
	again, err := Compile([]byte(source), testEvent, "matrix")
	if err != nil || !reflect.DeepEqual(plans, again) {
		t.Fatal("nondeterministic expansion", err)
	}
}
func TestMatrixRejectsMalformedAndBounds(t *testing.T) {
	for _, matrix := range []string{"{a: []}", "{a: [null]}", "{a: [{nested: value}]}", "{a: ['${{ github.sha }}']}", "{a: [1], exclude: [{b: 1}]}", "{include: []}"} {
		t.Run(matrix, func(t *testing.T) {
			_, err := Compile([]byte(job("    strategy: {matrix: "+matrix+"}\n", "      - run: echo ok\n")), testEvent, "x")
			if err == nil {
				t.Fatal("accepted", matrix)
			}
		})
	}
	_, err := Compile([]byte(job("    strategy: {matrix: {a: [1,2,3,4,5,6,7,8,9], b: [1,2,3,4,5,6,7,8,9], c: [1,2,3,4]}}\n", "      - run: echo ok\n")), testEvent, "x")
	if err == nil || !strings.Contains(err.Error(), "256") {
		t.Fatal(err)
	}
}

func TestConcurrencyEarlyIdentity(t *testing.T) {
	source := `name: pipeline
on: push
concurrency: '${{ github.workflow }}-${{ github.run_id }}'
jobs:
  build:
    runs-on: linux
    concurrency: '${{ github.job }}-${{ github.run_id }}'
    steps: [{run: 'echo ok'}]
`
	plans, err := Compile([]byte(source), testEvent, "run123")
	if err != nil || plans[0].Concurrency != "build-run123" || plans[0].WorkflowConcurrency != "pipeline-run123" {
		t.Fatal(plans, err)
	}
}
