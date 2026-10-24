package workflow

import (
	"errors"
	"testing"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestValidateDoesNotLetEventFiltersHideInvalidJobs(t *testing.T) {
	event := run.Event{Name: "push", Ref: "refs/heads/main", Repository: "test/project"}
	for _, tc := range []struct {
		name, source string
		valid        bool
	}{
		{"valid filtered", "on: {push: {branches: [other]}}\njobs: {a: {runs-on: homelab, steps: [{run: echo ok}]}}", true},
		{"unknown field", "on: {push: {branches: [other]}}\njobs: {a: {runs-on: homelab, unsupported: true}}", false},
		{"unknown need", "on: workflow_dispatch\njobs: {a: {runs-on: homelab, needs: missing, steps: [{run: echo ok}]}}", false},
		{"required dispatch", "on: {workflow_dispatch: {inputs: {value: {type: string, required: true}}}}\njobs: {a: {runs-on: homelab, steps: [{run: echo ok}]}}", true},
		{"invalid dispatch type", "on: {workflow_dispatch: {inputs: {value: {type: unsupported}}}}\njobs: {a: {runs-on: homelab, steps: [{run: echo ok}]}}", false},
		{"typed dispatch", "on: {workflow_dispatch: {inputs: {count: {type: number, required: true}, flag: {type: boolean}, env: {type: choice, options: [test, prod]}}}}\njobs: {a: {runs-on: homelab, steps: [{run: echo ok}]}}", true},
		{"invalid default", "on: {workflow_dispatch: {inputs: {count: {type: number, default: invalid}}}}\njobs: {a: {runs-on: homelab, steps: [{run: echo ok}]}}", false},
		{"null dispatch", "on: {workflow_dispatch: null}\njobs: {a: {runs-on: homelab, steps: [{run: echo ok}]}}", true},
		{"path filter", "on: {push: {paths: ['src/**']}}\njobs: {a: {runs-on: homelab, steps: [{run: echo ok}]}}", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Validate([]byte(tc.source), event); (err == nil) != tc.valid {
				t.Fatal(err)
			}
			if _, err := Compile([]byte(tc.source), event, "run"); !errors.Is(err, ErrNotTriggered) && !errors.Is(err, ErrFilesRequired) {
				t.Fatal("execution ignored filters", err)
			}
		})
	}
}
