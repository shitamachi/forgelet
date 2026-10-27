package storage

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/shitamachi/forgelet/internal/run"
	"github.com/shitamachi/forgelet/internal/workflow"
)

func callGraph(t *testing.T, s *Store, source, child, id string) map[string]run.Plan {
	t.Helper()
	plans, err := workflow.CompileFiles([]byte(source), run.Event{Name: "push", Repository: "me/repo"}, id, map[string][]byte{".github/workflows/child.yml": []byte(child)})
	if err != nil {
		t.Fatal(err)
	}
	return createGraph(t, s, id, plans...)
}

const simpleCall = `on: push
jobs:
  call:
    uses: ./.github/workflows/child.yml
  after:
    needs: call
    runs-on: small
    steps: [{run: echo after}]
`
const simpleChild = `on: workflow_call
jobs:
  test:
    runs-on: small
    steps: [{run: echo test}]
  cleanup:
    needs: test
    if: always()
    runs-on: small
    steps: [{run: echo cleanup}]
`

func TestCallFailureSkipAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name                              string
		childState, callState, afterState run.State
		skip                              bool
	}{
		{"success", run.Succeeded, run.Succeeded, run.Succeeded, false},
		{"failure", run.Failed, run.Failed, run.Skipped, false},
		{"interrupted", run.Interrupted, run.Failed, run.Skipped, false},
		{"skipped caller", run.Skipped, run.Skipped, run.Skipped, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTest(t)
			source := simpleCall
			if tc.skip {
				source = strings.Replace(source, "    uses:", "    if: false\n    uses:", 1)
			}
			plans := callGraph(t, s, source, simpleChild, "calls")
			reserved, err := s.Reserve(t.Context(), 1)
			if err != nil {
				t.Fatal(err)
			}
			if tc.skip {
				if len(reserved) != 0 {
					t.Fatal(reserved)
				}
			} else {
				if len(reserved) != 1 || reserved[0].Plan.Key != "call/test" {
					t.Fatal(reserved)
				}
				completeJob(t, s, reserved[0].ID, tc.childState, nil)
				reserved, err = s.Reserve(t.Context(), 1)
				if err != nil || len(reserved) != 1 || reserved[0].Plan.Key != "call/cleanup" {
					t.Fatal(reserved, err)
				}
				completeJob(t, s, reserved[0].ID, run.Succeeded, nil)
				reserved, err = s.Reserve(t.Context(), 1)
				if err != nil {
					t.Fatal(err)
				}
				if tc.afterState == run.Succeeded {
					if len(reserved) != 1 {
						t.Fatal(reserved)
					}
					completeJob(t, s, reserved[0].ID, run.Succeeded, nil)
				} else if len(reserved) != 0 {
					t.Fatal(reserved)
				}
			}
			for key, want := range map[string]run.State{"call": tc.callState, "after": tc.afterState} {
				j, err := s.GetJob(t.Context(), plans[key].JobID)
				if err != nil || j.State != want || key == "call" && j.PodUID != "" {
					t.Fatalf("%s=%s want %s (%v)", key, j.State, want, err)
				}
			}
		})
	}
}
func TestCallInputsAndOutputsSurviveDatabaseReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.db")
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	source := `on: push
jobs:
  before:
    runs-on: small
    steps: [{run: echo before}]
  call:
    needs: before
    uses: ./.github/workflows/child.yml
    with: {label: '${{ needs.before.outputs.label }}', flag: '${{ fromJSON(needs.before.outputs.flag) }}'}
  after:
    needs: call
    if: needs.call.outputs.label == 'persisted'
    runs-on: small
    steps: [{run: echo after}]
`
	child := `on:
  workflow_call:
    inputs:
      label: {type: string, required: true}
      flag: {type: boolean, default: false}
    outputs:
      label: {value: '${{ jobs.emit.outputs.label }}'}
jobs:
  emit:
    runs-on: small
    if: inputs.flag
    steps: [{run: 'echo ${{ inputs.label }}'}]
`
	plans := callGraph(t, s, source, child, "restart")
	jobs, err := s.Reserve(t.Context(), 1)
	if err != nil || len(jobs) != 1 {
		t.Fatal(jobs, err)
	}
	completeJob(t, s, jobs[0].ID, run.Succeeded, map[string]string{"label": "persisted", "flag": "true"})
	jobs, err = s.Reserve(t.Context(), 0)
	if err != nil || len(jobs) != 0 {
		t.Fatal(jobs, err)
	}
	gate, err := s.GetJob(t.Context(), plans["$start/call"].JobID)
	if err != nil || gate.State != run.Succeeded || gate.Plan.CallValues["label"] != "persisted" {
		t.Fatal(gate, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err = s.Reserve(t.Context(), 1)
	if err != nil || len(jobs) != 1 || jobs[0].Plan.Inputs["flag"] != true || jobs[0].Plan.Inputs["label"] != "persisted" {
		t.Fatal(jobs, err)
	}
	completeJob(t, s, jobs[0].ID, run.Succeeded, map[string]string{"label": "persisted"})
	jobs, err = s.Reserve(t.Context(), 1)
	if err != nil || len(jobs) != 1 || jobs[0].Plan.Key != "after" || jobs[0].Plan.NeedsContext["call"].Outputs["label"] != "persisted" {
		t.Fatal(jobs, err)
	}
}
func TestCallInputRuntimeTypeFailureDoesNotStartChildren(t *testing.T) {
	s := openTest(t)
	source := strings.Replace(simpleCall, "    uses:", "    needs: before\n    with: {flag: '${{ needs.before.outputs.flag }}'}\n    uses:", 1) + "  before:\n    runs-on: small\n    steps: [{run: echo before}]\n"
	child := "on:\n  workflow_call:\n    inputs:\n      flag: {type: boolean}\njobs:\n  test:\n    runs-on: small\n    if: always()\n    steps: [{run: echo forbidden}]\n"
	plans := callGraph(t, s, source, child, "invalid")
	jobs, err := s.Reserve(t.Context(), 1)
	if err != nil || len(jobs) != 1 {
		t.Fatal(jobs, err)
	}
	completeJob(t, s, jobs[0].ID, run.Succeeded, map[string]string{"flag": "false"})
	jobs, err = s.Reserve(t.Context(), 4)
	if err != nil || len(jobs) != 0 {
		t.Fatal(jobs, err)
	}
	gate, _ := s.GetJob(t.Context(), plans["$start/call"].JobID)
	called, _ := s.GetJob(t.Context(), plans["call/test"].JobID)
	if gate.State != run.Failed || !strings.Contains(gate.Result.Error, "requires boolean") || called.State != run.Skipped {
		t.Fatal(gate, called)
	}
}

func TestCalledMatrixUsesGlobalSlotsAndLogicalOutputs(t *testing.T) {
	s := openTest(t)
	child := `on:
  workflow_call:
    outputs: {value: {value: '${{ jobs.matrix.outputs.value }}'}}
jobs:
  matrix:
    runs-on: small
    strategy:
      max-parallel: 1
      matrix: {value: [one, two]}
    steps: [{run: echo matrix}]
`
	plans := callGraph(t, s, simpleCall, child, "matrix")
	jobs, err := s.Reserve(t.Context(), 4)
	if err != nil || len(jobs) != 1 || jobs[0].Plan.Key != "call/matrix[0]" {
		t.Fatal(jobs, err)
	}
	completeJob(t, s, jobs[0].ID, run.Succeeded, map[string]string{"value": "one"})
	jobs, err = s.Reserve(t.Context(), 4)
	if err != nil || len(jobs) != 1 || jobs[0].Plan.Key != "call/matrix[1]" {
		t.Fatal(jobs, err)
	}
	completeJob(t, s, jobs[0].ID, run.Succeeded, map[string]string{"value": "two"})
	jobs, err = s.Reserve(t.Context(), 1)
	if err != nil || len(jobs) != 1 || jobs[0].Plan.Key != "after" {
		t.Fatal(jobs, err)
	}
	result, err := s.GetJob(t.Context(), plans["call"].JobID)
	if err != nil || result.Result.Outputs["value"] != "two" {
		t.Fatal(result, err)
	}
}

func TestWorkflowOutputFromJobWithoutOutputsIsEmpty(t *testing.T) {
	s := openTest(t)
	child := "on:\n  workflow_call:\n    outputs: {value: {value: '${{ jobs.test.outputs.missing }}'}}\njobs:\n  test:\n    runs-on: small\n    steps: [{run: echo no-outputs}]\n"
	plans := callGraph(t, s, simpleCall, child, "empty-output")
	jobs, err := s.Reserve(t.Context(), 1)
	if err != nil || len(jobs) != 1 {
		t.Fatal(jobs, err)
	}
	completeJob(t, s, jobs[0].ID, run.Succeeded, nil)
	jobs, err = s.Reserve(t.Context(), 1)
	if err != nil || len(jobs) != 1 || jobs[0].Plan.Key != "after" {
		t.Fatal(jobs, err)
	}
	result, err := s.GetJob(t.Context(), plans["call"].JobID)
	if err != nil || result.State != run.Succeeded || result.Result.Outputs["value"] != "" || result.Result.Error != "" {
		t.Fatal(result, err)
	}
}
