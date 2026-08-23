package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/run/plan"
	"github.com/shitamachi/forgelet/internal/security/identity"
)

// fakeCP records calls and serves canned data.
type fakeCP struct {
	mu       sync.Mutex
	secrets  map[string]string
	plan     plan.Plan
	reported []JobResult
	fetches  int
}

func (f *fakeCP) FetchPlan(context.Context, identity.Identity) (plan.Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetches++
	return f.plan, nil
}

func (f *fakeCP) FetchSecrets(_ context.Context, _ identity.Identity, refs []plan.SecretRef) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for _, r := range refs {
		if v, ok := f.secrets[r.Scope+"/"+r.Name]; ok {
			out[r.Scope+"/"+r.Name] = v
		}
	}
	return out, nil
}

func (f *fakeCP) ReportJob(_ context.Context, _ identity.Identity, r JobResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reported = append(f.reported, r)
	return nil
}

type capturedLogs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

type mu_safeWriter struct{ c *capturedLogs }

func (w *mu_safeWriter) Write(p []byte) (int, error) {
	w.c.mu.Lock()
	defer w.c.mu.Unlock()
	return w.c.buf.Write(p)
}

func (c *capturedLogs) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

func testID() identity.Identity {
	return identity.Identity{
		Audience: identity.Audience, Namespace: "forgelet-jobs", PodUID: "pod-1",
		JobRunID: model.JobRunID("01JTEST0000000000000000000X"),
		Scopes:   []string{identity.ScopePlanRead, identity.ScopeSecretsRead, identity.ScopeStatusWrite},
	}
}

func newEngine(t *testing.T, cp ControlPlane) (*Engine, *capturedLogs) {
	t.Helper()
	logs := &capturedLogs{}
	return &Engine{
		CP:      cp,
		WorkDir: t.TempDir(),
		Grace:   500 * time.Millisecond,
		Logger:  slog.New(slog.NewJSONHandler(&mu_safeWriter{logs}, nil)),
	}, logs
}

func oneStepPlan(steps ...plan.Step) plan.Plan {
	return plan.Plan{
		JobRunID:    "01JTEST0000000000000000000X",
		RunnerClass: "small",
		Repository:  model.RepositoryRef{Provider: "github", Owner: "o", Name: "r"},
		SHA:         "abc",
		Ref:         "refs/heads/main",
		Steps:       steps,
	}
}

func step(id, script string) plan.Step {
	return plan.Step{ID: id, Run: plan.RunStep{Script: script}}
}

// AC 1: steps share the filesystem and runtime ENV.
func TestStepsShareWorkspaceAndEnv(t *testing.T) {
	cp := &fakeCP{}
	e, logs := newEngine(t, cp)
	p := oneStepPlan(
		step("writer", "echo marker=hello >> $GITHUB_ENV\necho built=shared >> $GITHUB_OUTPUT\nprintf 'shared' > artifact.txt\n"),
		step("reader", "test \"$(cat artifact.txt)\" = shared && test \"$marker\" = hello\n"),
	)

	result, err := e.Run(context.Background(), testID(), p)
	if err != nil || !result.Success {
		t.Fatalf("run: %v %+v", err, result)
	}
	if !strings.Contains(logs.String(), "step output") || !strings.Contains(logs.String(), "built") {
		t.Error("step outputs were not logged")
	}
	// Reader succeeded only because both the file and the env var survived.
}

// AC 1b: GITHUB_PATH makes new scripts executable in later steps.
func TestGithubPathPrepends(t *testing.T) {
	cp := &fakeCP{}
	e, _ := newEngine(t, cp)
	p := oneStepPlan(
		step("install", "mkdir -p bin\necho '#!/bin/bash\necho from-custom-tool' > bin/custom-tool\nchmod +x bin/custom-tool\necho \"$PWD/bin\" >> $GITHUB_PATH\n"),
		step("use", "test \"$(custom-tool)\" = from-custom-tool\n"),
	)
	if _, err := e.Run(context.Background(), testID(), p); err != nil {
		t.Fatalf("run: %v", err)
	}
}

// AC 2: fetched secrets and ::add-mask:: values never appear in logs.
func TestSecretMasking(t *testing.T) {
	cp := &fakeCP{secrets: map[string]string{"repository/REGISTRY_TOKEN": "ghs_supersecret123"}}
	e, logs := newEngine(t, cp)
	p := oneStepPlan(step("leak", "echo token=$REGISTRY_TOKEN\necho ::add-mask::dynamic-mask-me\necho masked=dynamic-mask-me\n"))
	p.SecretRefs = []plan.SecretRef{{Scope: "repository", Name: "REGISTRY_TOKEN", Env: "REGISTRY_TOKEN"}}
	if _, err := e.Run(context.Background(), testID(), p); err != nil {
		t.Fatalf("run: %v", err)
	}
	out := logs.String()
	for _, secret := range []string{"ghs_supersecret123", "dynamic-mask-me"} {
		if strings.Contains(out, secret) {
			t.Errorf("secret %q leaked into logs:\n%s", secret, out)
		}
	}
	if !strings.Contains(out, "***") {
		t.Error("logs do not contain the mask marker")
	}
}

// AC 3: a failing step stops the sequence and reports failure.
func TestFailureStopsAndReports(t *testing.T) {
	cp := &fakeCP{}
	e, _ := newEngine(t, cp)
	p := oneStepPlan(
		step("ok", "echo fine"),
		step("boom", "echo to-stdout; echo to-stderr >&2; exit 3"),
		step("never", "echo should-not-run"),
	)

	result, err := e.Run(context.Background(), testID(), p)
	if !errors.Is(err, ErrStepFailed) {
		t.Fatalf("err = %v, want ErrStepFailed", err)
	}
	if result.Success || result.Steps[1].ExitCode != 3 {
		t.Errorf("result = %+v", result)
	}
	// Two steps ran; the third is recorded as skipped without executing.
	if len(result.Steps) != 3 {
		t.Errorf("steps after failure: %+v", result.Steps)
	}
	if result.Steps[2].StepID != "never" || result.Steps[2].Outcome != "skipped" {
		t.Errorf("remaining step = %+v, want skipped record", result.Steps[2])
	}
	if cp.reported[0].Success {
		t.Error("failure reported as success")
	}
	// The third step must never have produced a script file.
	if _, err := os.Stat(filepath.Join(e.WorkDir, ".forgelet", "step-never.sh")); !errors.Is(err, os.ErrNotExist) {
		t.Error("step after failure ran")
	}
}

// AC 4: cancellation kills the process group within grace.
func TestCancellationKillsProcessGroup(t *testing.T) {
	cp := &fakeCP{}
	e, _ := newEngine(t, cp)
	p := oneStepPlan(step("long", "sleep 30 &\nwait $!\n"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := e.Run(ctx, testID(), p)
		if !errors.Is(err, ErrCancelled) {
			t.Errorf("err = %v, want ErrCancelled", err)
		}
	}()
	time.Sleep(700 * time.Millisecond) // let the step start
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("job did not terminate after cancellation")
	}
	cp.mu.Lock()
	cancelled := cp.reported[len(cp.reported)-1].Cancelled
	cp.mu.Unlock()
	if !cancelled {
		t.Error("cancelled job must report Cancelled=true")
	}
}

func TestTimeoutBehavesAsCancel(t *testing.T) {
	cp := &fakeCP{}
	e, _ := newEngine(t, cp)
	p := oneStepPlan(step("slow", "sleep 30"))

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := e.Run(ctx, testID(), p)
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("err = %v, want ErrCancelled", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout not enforced, took %s", elapsed)
	}
}

func TestFetchSecretsErrorStopsRun(t *testing.T) {
	e, _ := newEngine(t, failCP{})
	p := oneStepPlan(step("s", "echo hi"))
	p.SecretRefs = []plan.SecretRef{{Scope: "repository", Name: "X"}}
	if _, err := e.Run(context.Background(), testID(), p); err == nil {
		t.Fatal("secret fetch failure must stop the run")
	}
}

type failCP struct{}

func (failCP) FetchPlan(context.Context, identity.Identity) (plan.Plan, error) {
	return plan.Plan{}, nil
}
func (failCP) FetchSecrets(context.Context, identity.Identity, []plan.SecretRef) (map[string]string, error) {
	return nil, errors.New("injected")
}
func (failCP) ReportJob(context.Context, identity.Identity, JobResult) error { return nil }

// ReportJob failures are logged but do not change the outcome.
func TestReportFailureTolerated(t *testing.T) {
	e, _ := newEngine(t, reportFailCP{&fakeCP{}})
	p := oneStepPlan(step("s", "echo hi"))
	result, err := e.Run(context.Background(), testID(), p)
	if err != nil || !result.Success {
		t.Fatalf("run failed on report error: %v %+v", err, result)
	}
}

type reportFailCP struct{ *fakeCP }

func (reportFailCP) ReportJob(context.Context, identity.Identity, JobResult) error {
	return errors.New("injected report failure")
}

// Sanity: the JSON log lines parse and carry identity fields.
func TestStructuredLogShape(t *testing.T) {
	cp := &fakeCP{}
	e, logs := newEngine(t, cp)
	p := oneStepPlan(step("s", "echo hello-logs"))
	if _, err := e.Run(context.Background(), testID(), p); err != nil {
		t.Fatalf("run: %v", err)
	}
	var sawStep bool
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("non-JSON log line %q: %v", line, err)
		}
		if m["step"] == "s" && m["message"] == "hello-logs" {
			sawStep = true
			if m["jobRun"] != string(testID().JobRunID) {
				t.Errorf("log lacks jobRun: %v", m)
			}
		}
	}
	if !sawStep {
		t.Error("step output line not found in logs")
	}
}
