package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shitamachi/forgelet/internal/run/plan"
)

func outcomeOf(t *testing.T, r JobResult, id string) (string, string) {
	t.Helper()
	for _, s := range r.Steps {
		if s.StepID == id {
			return s.Outcome, s.Conclusion
		}
	}
	t.Fatalf("step %q missing from %+v", id, r.Steps)
	return "", ""
}

// A false `if:` records the skipped outcome and the job keeps going.
func TestStepIfFalseSkipsButJobContinues(t *testing.T) {
	cp := &fakeCP{}
	e, _ := newEngine(t, cp)
	p := oneStepPlan(
		step("a", "echo ran-a > ran.txt\n"),
		step("b", "exit 1"),
	)
	p.Steps[1].If = "false"
	p.Steps = append(p.Steps,
		step("c", "test -f ran.txt\n"),
		func() plan.Step {
			s := step("d", "test \"${{ github.sha }}\" = abc\n")
			s.If = "${{ github.event_name == '' || github.sha == 'abc' }}"
			return s
		}(),
	)

	result, err := e.Run(context.Background(), testID(), p)
	if err != nil || !result.Success {
		t.Fatalf("run: %v %+v", err, result)
	}
	if o, c := outcomeOf(t, result, "b"); o != outcomeSkipped || c != conclusionSkip {
		t.Errorf("b = %s/%s, want skipped/skipped", o, c)
	}
	if o, _ := outcomeOf(t, result, "c"); o != outcomeSuccess {
		t.Errorf("c must still run, got %s", o)
	}
	if o, _ := outcomeOf(t, result, "d"); o != outcomeSuccess {
		t.Errorf("d must run on interpolated true condition, got %s", o)
	}
}

// Outputs and hashFiles are visible to later step conditions.
func TestStepConditionsSeeOutputsAndWorkspace(t *testing.T) {
	cp := &fakeCP{}
	e, _ := newEngine(t, cp)
	work := e.WorkDir
	if err := os.WriteFile(filepath.Join(work, "deps.lock"), []byte("x: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := oneStepPlan(
		step("emit", "echo color=blue >> $GITHUB_OUTPUT\n"),
		func() plan.Step {
			s := step("use-output", "exit 0")
			s.If = "steps.emit.outputs.color == 'blue'"
			return s
		}(),
		func() plan.Step { s := step("hash", "exit 0"); s.If = "hashFiles('*.lock') != ''"; return s }(),
		func() plan.Step { s := step("no-hash", "exit 0"); s.If = "hashFiles('*.nomatch') != ''"; return s }(),
	)

	result, err := e.Run(context.Background(), testID(), p)
	if err != nil || !result.Success {
		t.Fatalf("run: %v %+v", err, result)
	}
	for _, id := range []string{"use-output", "hash"} {
		if o, _ := outcomeOf(t, result, id); o != outcomeSuccess {
			t.Errorf("%s must run, got %s", id, o)
		}
	}
	if o, _ := outcomeOf(t, result, "no-hash"); o != outcomeSkipped {
		t.Errorf("no-hash must be skipped, got %s", o)
	}
}

// continue-on-error folds a failing step into success while recording the
// failure outcome; the following step still runs.
func TestContinueOnErrorKeepsJobGreen(t *testing.T) {
	cp := &fakeCP{}
	e, logs := newEngine(t, cp)
	flaky := step("flaky", "echo boom >&2\nexit 3\n")
	flaky.ContinueOnError = true
	p := oneStepPlan(flaky, step("after", "exit 0"))

	result, err := e.Run(context.Background(), testID(), p)
	if err != nil || !result.Success {
		t.Fatalf("run: %v %+v", err, result)
	}
	o, c := outcomeOf(t, result, "flaky")
	if o != outcomeFailure || c != conclusionOK {
		t.Errorf("flaky = %s/%s, want failure/success", o, c)
	}
	if o, _ := outcomeOf(t, result, "after"); o != outcomeSuccess {
		t.Errorf("after must run, got %s", o)
	}
	if !strings.Contains(logs.String(), "continue-on-error") {
		t.Error("continuation should be logged")
	}
}

// A hard failure marks every remaining step skipped.
func TestHardFailureSkipsRemainingSteps(t *testing.T) {
	cp := &fakeCP{}
	e, _ := newEngine(t, cp)
	p := oneStepPlan(step("ok", "exit 0"), step("boom", "exit 9"), step("never", "exit 0"))

	result, err := e.Run(context.Background(), testID(), p)
	if err == nil || result.Success {
		t.Fatalf("run must fail: %v %+v", err, result)
	}
	if o, _ := outcomeOf(t, result, "never"); o != outcomeSkipped {
		t.Errorf("never = %s, want skipped", o)
	}
}

// A condition that cannot be evaluated fails the job like a failed step.
func TestBrokenConditionFailsJob(t *testing.T) {
	cp := &fakeCP{}
	e, _ := newEngine(t, cp)
	bad := step("bad", "exit 0")
	bad.If = "${{ nonexistent_context.x == 1 }}"
	p := oneStepPlan(bad, step("tail", "exit 0"))

	result, err := e.Run(context.Background(), testID(), p)
	if err == nil || result.Success {
		t.Fatalf("broken condition must fail the job: %v %+v", err, result)
	}
	if !strings.Contains(result.Error, "nonexistent_context") {
		t.Errorf("error = %q", result.Error)
	}
	if o, _ := outcomeOf(t, result, "tail"); o != outcomeSkipped {
		t.Errorf("tail = %s, want skipped", o)
	}
}

// Expressions inside run scripts and env values are rendered before execution.
func TestScriptAndEnvInterpolation(t *testing.T) {
	cp := &fakeCP{}
	e, _ := newEngine(t, cp)
	s := step("greet", "test \"$MSG-$WHO\" = hi-executor && test \"$STATIC\" = plain\n")
	s.Run.Env = map[string]string{"MSG": "${{ 'hi' }}", "STATIC": "plain"}
	p := oneStepPlan(s)
	p.Env = map[string]string{"WHO": "${{ github.actor }}"}
	p.Actor = "executor"

	if _, err := e.Run(context.Background(), testID(), p); err != nil {
		t.Fatalf("run: %v", err)
	}
}
