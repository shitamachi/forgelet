package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGithubStateAndSummary(t *testing.T) {
	cp := &fakeCP{}
	e, _ := newEngine(t, cp)
	p := oneStepPlan(
		step("writer", "echo \"FOO=bar\" >> $GITHUB_STATE\necho \"hello summary\" >> $GITHUB_STEP_SUMMARY\n"),
		step("reader", "test \"$STATE_FOO\" = bar\n"),
	)
	result, err := e.Run(context.Background(), testID(), p)
	if err != nil || !result.Success {
		t.Fatalf("run: %v %+v", err, result)
	}
	// Verify writer's summary file contains hello summary
	data, _ := os.ReadFile(filepath.Join(e.WorkDir, ".forgelet", "step-0.summary"))
	if !strings.Contains(string(data), "hello summary") {
		t.Errorf("GITHUB_STEP_SUMMARY not written, got %q", string(data))
	}
}

func TestCommandPropertiesParsed(t *testing.T) {
	cp := &fakeCP{}
	e, logs := newEngine(t, cp)
	p := oneStepPlan(step("ann", "echo \"::warning file=app.js,line=10::my warning\"\n"))
	if _, err := e.Run(context.Background(), testID(), p); err != nil {
		t.Fatalf("run: %v", err)
	}
	out := logs.String()
	if !strings.Contains(out, "app.js") || !strings.Contains(out, "my warning") {
		t.Errorf("warning command properties not logged: %s", out)
	}
}
