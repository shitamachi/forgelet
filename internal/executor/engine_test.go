package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestExecuteSharedWorkspaceAndFileCommands(t *testing.T) {
	workspace := t.TempDir()
	plan := run.Plan{SHA: strings.Repeat("a", 40), Ref: "refs/heads/main", Repository: "owner/repo", Event: []byte(`{"sender":{"login":"alice"}}`), Env: map[string]string{"SCOPE": "job", "SHA_COPY": "${{ github.sha }}"}, Steps: []run.Step{
		{ID: "first", Run: `printf 'shared' > shared.txt
mkdir tools
printf '#!/bin/sh\necho executable\n' > tools/tool
chmod +x tools/tool
printf '%s\n' "$GITHUB_WORKSPACE/tools" >> "$GITHUB_PATH"
printf 'VALUE=next\nMULTI<<END\none\ntwo\nEND\n' >> "$GITHUB_ENV"
printf 'answer=42\nblock<<END\na\nb\nEND\n' >> "$GITHUB_OUTPUT"
export TRANSIENT=not-shared
test "$SCOPE" = step
test "$SHA_COPY" = "$GITHUB_SHA"
test -f "$GITHUB_EVENT_PATH"`, Env: map[string]string{"SCOPE": "step"}},
		{ID: "second", Shell: "sh", Run: `test "$(cat shared.txt)" = shared
test "$VALUE" = next
test "$SCOPE" = job
test -z "${TRANSIENT:-}"
test "$MULTI" = "$(printf 'one\ntwo')"
test "$(tool)" = executable
printf '%s\n' '${{ steps.first.outputs.answer }} ${{ github.actor }} ${{ github.ref_name }} ${{ github.event_name }} ${{ env.VALUE }}'
test "$PREVIOUS" = next`, Env: map[string]string{"PREVIOUS": "${{ env.VALUE }}"}},
	}}
	var logs bytes.Buffer
	result, err := Execute(t.Context(), plan, Options{Workspace: workspace, Log: func(_ string, data []byte) error { _, err := logs.Write(data); return err }})
	if err != nil || result.State != run.Succeeded || len(result.Steps) != 2 {
		t.Fatalf("result=%+v error=%v logs=%s", result, err, logs.String())
	}
	if result.Steps[0].Outputs["answer"] != "42" || result.Steps[0].Outputs["block"] != "a\nb" || !strings.Contains(logs.String(), "42 alice main push next") {
		t.Fatalf("outputs=%+v logs=%s", result.Steps, logs.String())
	}
}

func TestExecuteFailureAndShellBehavior(t *testing.T) {
	for _, tc := range []struct {
		name, script, shell string
		state               run.State
		code                int
	}{
		{"explicit exit", "exit 7", "sh", run.Failed, 7},
		{"bash pipefail", "false | true", "bash", run.Failed, 1},
		{"sh pipeline", "false | true", "sh", run.Succeeded, 0},
		{"bash error stops", "false\necho must-not-run", "bash", run.Failed, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			plan := run.Plan{Steps: []run.Step{{ID: "first", Run: tc.script, Shell: tc.shell}, {ID: "second", Run: "echo second"}}}
			result, err := Execute(t.Context(), plan, Options{Workspace: t.TempDir(), Log: func(_ string, data []byte) error { _, err := logs.Write(data); return err }})
			if err != nil || result.State != tc.state || result.Steps[0].ExitCode != tc.code {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			if tc.state == run.Failed && (len(result.Steps) != 2 || result.Steps[1].State != run.Skipped || strings.Contains(logs.String(), "must-not-run") || strings.Contains(logs.String(), "second")) {
				t.Fatalf("executed after failure: %+v %s", result, logs.String())
			}
		})
	}
}

func TestExecuteTimeoutAndCancellationKillChildren(t *testing.T) {
	for _, timeout := range []bool{true, false} {
		t.Run(fmt.Sprintf("timeout=%t", timeout), func(t *testing.T) {
			workspace := t.TempDir()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			step := run.Step{ID: "sleep", Run: "sleep 30 &\necho $! > child.pid\nwait"}
			if timeout {
				step.TimeoutSeconds = 1
			} else {
				go func() {
					timer := time.NewTimer(150 * time.Millisecond)
					defer timer.Stop()
					select {
					case <-timer.C:
						cancel()
					case <-ctx.Done():
					}
				}()
			}
			started := time.Now()
			result, err := Execute(ctx, run.Plan{Steps: []run.Step{step}}, Options{Workspace: workspace})
			want := run.Cancelled
			if timeout {
				want = run.TimedOut
			}
			if err != nil || result.State != want || time.Since(started) > 4*time.Second {
				t.Fatalf("result=%+v error=%v duration=%s", result, err, time.Since(started))
			}
			data, err := os.ReadFile(filepath.Join(workspace, "child.pid"))
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(time.Second)
			for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
				t.Fatalf("child %d survives cancellation: %v", pid, err)
			}
		})
	}
}

func TestExecuteRejectsWorkspaceEscape(t *testing.T) {
	workspace, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspace, "link")); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{"../outside", outside, "link", "link/new"} {
		t.Run(directory, func(t *testing.T) {
			plan := run.Plan{Steps: []run.Step{{ID: "test", Run: "touch bad", WorkingDirectory: directory}}}
			result, err := Execute(t.Context(), plan, Options{Workspace: workspace})
			if err == nil || result.State != run.Failed {
				t.Fatalf("result=%+v error=%v", result, err)
			}
		})
	}
}

func TestExecuteMasksDynamicAndMultilineSecrets(t *testing.T) {
	var logs bytes.Buffer
	plan := run.Plan{Steps: []run.Step{{ID: "secret", Run: "printf 'ab'; printf 'cd\\n'; printf 'one\\ntwo\\n'; printf '::add-mask::new-secret\\n'; printf 'new-secret\\n'"}}}
	result, err := Execute(t.Context(), plan, Options{Workspace: t.TempDir(), Masks: []string{"abcd", "one\ntwo"}, Log: func(_ string, data []byte) error { _, err := logs.Write(data); return err }})
	if err != nil || result.State != run.Succeeded {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if logs.String() != "***\n***\n***\n" {
		t.Fatalf("unexpected masked logs %q", logs.String())
	}
}

func TestExecuteLogFailureStopsProcess(t *testing.T) {
	start := time.Now()
	result, err := Execute(t.Context(), run.Plan{Steps: []run.Step{{ID: "test", Run: "echo log\nsleep 30"}}}, Options{Workspace: t.TempDir(), Log: func(_ string, _ []byte) error { return errors.New("log unavailable") }})
	if err == nil || result.State != run.Failed || time.Since(start) > 3*time.Second {
		t.Fatalf("result=%+v error=%v duration=%s", result, err, time.Since(start))
	}
}

func TestExecuteSlowLogsDoNotFailCompletedStep(t *testing.T) {
	var logs bytes.Buffer
	plan := run.Plan{Steps: []run.Step{{ID: "first", Run: "echo first"}, {ID: "second", Run: "echo second"}}}
	result, err := Execute(t.Context(), plan, Options{
		Workspace: t.TempDir(),
		ContextLog: func(ctx context.Context, _ string, data []byte) error {
			timer := time.NewTimer(750 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				_, err := logs.Write(data)
				return err
			}
		},
	})
	if err != nil || result.State != run.Succeeded || len(result.Steps) != 2 || logs.String() != "first\nsecond\n" {
		t.Fatalf("result=%+v error=%v logs=%q", result, err, logs.String())
	}
}

func TestExecuteCancellationInterruptsSlowLogUpload(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	uploadStarted := make(chan struct{})
	go func() {
		select {
		case <-uploadStarted:
			cancel()
		case <-ctx.Done():
		}
	}()
	started := time.Now()
	result, err := Execute(ctx, run.Plan{Steps: []run.Step{{ID: "test", Run: "echo log\nsleep 30"}}}, Options{
		Workspace: t.TempDir(),
		ContextLog: func(logCtx context.Context, _ string, _ []byte) error {
			close(uploadStarted)
			<-logCtx.Done()
			return logCtx.Err()
		},
	})
	if err != nil || result.State != run.Cancelled || time.Since(started) > 2*time.Second {
		t.Fatalf("result=%+v error=%v duration=%s", result, err, time.Since(started))
	}
}

func TestExecuteReapsBackgroundChildAfterShellExit(t *testing.T) {
	workspace := t.TempDir()
	started := time.Now()
	result, err := Execute(t.Context(), run.Plan{Steps: []run.Step{{ID: "test", Run: "sleep 30 &\necho $! > child.pid\necho complete"}}}, Options{Workspace: workspace})
	if err != nil || result.State != run.Succeeded || time.Since(started) > 2*time.Second {
		t.Fatalf("result=%+v error=%v duration=%s", result, err, time.Since(started))
	}
	data, err := os.ReadFile(filepath.Join(workspace, "child.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Fatalf("background child %d survives shell exit: %v", pid, err)
	}
}

func TestCheckoutFixedCommitAndNoPersistedCredentials(t *testing.T) {
	source := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = source
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		data, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, data)
		}
		return strings.TrimSpace(string(data))
	}
	git("init", "--quiet")
	if err := os.WriteFile(filepath.Join(source, "version"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "version")
	git("commit", "--quiet", "-m", "first")
	sha := git("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(source, "version"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	git("commit", "--quiet", "-am", "second")
	for _, tc := range []struct {
		path  string
		depth int
	}{{"", 0}, {"", 1}, {"src", 0}, {"src", 1}} {
		t.Run(fmt.Sprintf("path=%q/depth=%d", tc.path, tc.depth), func(t *testing.T) {
			workspace := t.TempDir()
			plan := run.Plan{SHA: sha, Steps: []run.Step{{ID: "checkout", Checkout: &run.Checkout{Path: tc.path, FetchDepth: tc.depth}}, {ID: "test", WorkingDirectory: tc.path, Run: "test \"$(cat version)\" = old\ntest \"$(git rev-parse HEAD)\" = \"$GITHUB_SHA\""}}}
			result, err := Execute(t.Context(), plan, Options{Workspace: workspace, CloneURL: source})
			if err != nil || result.State != run.Succeeded {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			config, err := os.ReadFile(filepath.Join(workspace, tc.path, ".git", "config"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(strings.ToLower(string(config)), "extraheader") || strings.Contains(string(config), "credential") {
				t.Fatalf("unexpected credential configuration: %s", config)
			}
		})
	}
}

func TestInterpolationAndFileCommandErrors(t *testing.T) {
	for _, expression := range []string{"${{ env.A", "${{ secrets.A }}", "${{ github.token }}", "${{ steps.missing.outputs.a }}"} {
		if _, err := interpolate(expression, run.Plan{}, nil, nil); err == nil {
			t.Errorf("accepted %q", expression)
		}
	}
	for _, text := range []string{"BROKEN", "A<<\nx", "A<<END\nx", "bad key=x"} {
		if _, err := parseCommandPairs(text); err == nil {
			t.Errorf("accepted file command %q", text)
		}
	}
	pairs, err := parseCommandPairs("A=x=y\r\nB<<EOF\r\nfirst\r\nsecond\r\nEOF\r\nC=\n")
	if err != nil || pairs["A"] != "x=y" || pairs["B"] != "first\nsecond" || pairs["C"] != "" {
		t.Fatalf("pairs=%+v error=%v", pairs, err)
	}
	for _, script := range []string{"echo GITHUB_TOKEN=bad >> \"$GITHUB_ENV\"", "echo BROKEN >> \"$GITHUB_OUTPUT\""} {
		result, err := Execute(t.Context(), run.Plan{Steps: []run.Step{{ID: "test", Run: script}}}, Options{Workspace: t.TempDir()})
		if err == nil || result.State != run.Failed {
			t.Fatalf("result=%+v error=%v", result, err)
		}
	}
}

func TestExecuteConcurrentIsolation(t *testing.T) {
	for index := range 6 {
		t.Run(strconv.Itoa(index), func(t *testing.T) {
			t.Parallel()
			identity := "job-" + strconv.Itoa(index)
			secret := "secret-" + strconv.Itoa(index)
			foreign := "secret-" + strconv.Itoa((index+1)%6)
			workspace := t.TempDir()
			plan := run.Plan{Env: map[string]string{"IDENTITY": identity, "SECRET": secret, "FOREIGN": foreign}, Steps: []run.Step{
				{ID: "make", Run: "echo \"$IDENTITY\" > marker\necho \"VALUE=$IDENTITY\" >> \"$GITHUB_ENV\"\necho \"value=$IDENTITY\" >> \"$GITHUB_OUTPUT\"\nsleep 0.1\nprintf '%s\\n' \"$SECRET\" \"$FOREIGN\""},
				{ID: "check", Run: "test \"$(cat marker)\" = \"$IDENTITY\"\ntest \"$VALUE\" = \"$IDENTITY\"\ntest '${{ steps.make.outputs.value }}' = \"$IDENTITY\""},
			}}
			var logs bytes.Buffer
			result, err := Execute(t.Context(), plan, Options{Workspace: workspace, Masks: []string{secret}, Log: func(_ string, data []byte) error { _, err := logs.Write(data); return err }})
			if err != nil || result.State != run.Succeeded || len(result.Steps) != 2 || result.Steps[0].Outputs["value"] != identity {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			if logs.String() != "***\n"+foreign+"\n" {
				t.Fatalf("mask leaked across jobs: %q", logs.String())
			}
		})
	}
}

func TestExecutorTransportEnvironmentIsNotInheritedBySteps(t *testing.T) {
	for _, name := range []string{"FORGELET_URL", "FORGELET_CONTROL_TIMEOUT", "FORGELET_REGISTRY_CONFIG", "TOKEN_FILE", "POD_UID", "JOB_ID", "WORKSPACE"} {
		t.Setenv(name, "internal-value")
	}
	t.Setenv("VISIBLE_SETTING", "preserved")
	result, err := Execute(t.Context(), run.Plan{Steps: []run.Step{{ID: "env", Shell: "sh", Run: `test -z "${FORGELET_URL:-}${FORGELET_CONTROL_TIMEOUT:-}${FORGELET_REGISTRY_CONFIG:-}${TOKEN_FILE:-}${POD_UID:-}${JOB_ID:-}${WORKSPACE:-}"
test "$VISIBLE_SETTING" = preserved
test -n "$GITHUB_WORKSPACE"`}}}, Options{Workspace: t.TempDir()})
	if err != nil || result.State != run.Succeeded {
		t.Fatal(result, err)
	}
}
