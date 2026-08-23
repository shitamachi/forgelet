// Package syntax parses GitHub Actions workflow files into a strictly
// validated AST. Only the declared M0 subset is accepted; anything else is
// reported with file, line and column — never silently ignored (spec 0001
// FR-2.4). Expression placeholders are preserved verbatim; evaluation
// belongs to the expression engine (spec 0007).
package syntax

import (
	"fmt"
	"strings"
)

// Diagnostic is one problem found in a workflow file.
type Diagnostic struct {
	File    string
	Line    int
	Column  int
	Path    string // e.g. "jobs.test.steps[0]"
	Message string
}

func (d Diagnostic) String() string {
	return fmt.Sprintf("%s:%d:%d: %s: %s", d.File, d.Line, d.Column, d.Path, d.Message)
}

// Error carries one or more diagnostics; a failed parse produces no partial
// AST.
type Error struct {
	Diagnostics []Diagnostic
}

func (e *Error) Error() string {
	msgs := make([]string, 0, len(e.Diagnostics))
	for _, d := range e.Diagnostics {
		msgs = append(msgs, d.String())
	}
	return fmt.Sprintf("workflow syntax: %s", strings.Join(msgs, "; "))
}

// Position is a source location.
type Position struct {
	Line   int
	Column int
}

// Workflow is the parsed M0 workflow AST.
type Workflow struct {
	File string
	Name string
	On   Triggers
	Jobs []*Job // document order
}

// Triggers holds the declared triggers. V1 supports push, pull_request
// (base-branch filters) and schedule (cron expressions).
type Triggers struct {
	Push        *PushTrigger
	PullRequest *PushTrigger // same filter shape, applied to the base branch
	Schedule    []string     // cron expressions
}

// PushTrigger is the `on.push` filter set.
type PushTrigger struct {
	Branches       []string
	BranchesIgnore []string
}

// Job is one workflow job.
type Job struct {
	ID     string
	Pos    Position
	Name   string
	RunsOn string
	Needs  []string
	Matrix map[string][]string // axis name -> values (V1: no include/exclude)
	Env    map[string]string
	Steps  []*Step
}

// Step is one `run` step.
type Step struct {
	Pos  Position
	Name string
	Run  string
	Env  map[string]string
}

const subsetMessage = "field is not in the supported subset"
