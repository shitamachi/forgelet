package runner

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestActSandboxCopyFailureRemovesOnlyOwnedSandbox(t *testing.T) {
	dir := t.TempDir()
	// Bundle discovery validates regular files without executing them on the host.
	for _, name := range []string{"forgelet-runner-linux-arm64", "forgelet-act-linux-arm64", "forgelet-runner-linux-amd64", "forgelet-act-linux-amd64"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("worker"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	var calls [][]string
	d := &dockerExecutor{binaryDir: dir, owner: "our-device", command: func(ctx context.Context, args ...string) *exec.Cmd {
		calls = append(calls, append([]string(nil), args...))
		switch args[0] {
		case "cp":
			return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 1")
		case "ps":
			return exec.CommandContext(ctx, "/bin/sh", "-c", "echo our-container")
		}
		return exec.CommandContext(ctx, "/usr/bin/true")
	}}
	u, _ := url.Parse("https://control.example")
	env := run.RunnerContainer{Kernel: "act", CPUs: 4, MemoryBytes: 4 << 30, Platform: "linux/amd64", Image: "engine@sha256:" + strings.Repeat("a", 64)}
	token := "frja_" + strings.Repeat("b", 64)
	result := d.executeAct(t.Context(), &client{url: u}, run.ExternalGrant{Container: &env}, token, func(Status) {})
	if result.State != "failed" || len(calls) != 4 {
		t.Fatalf("result=%+v calls=%v", result, calls)
	}
	joined := strings.Join(calls[0], " ")
	if !strings.Contains(joined, "--cpuset-cpus 0-3") || strings.Contains(joined, "--mount") || strings.Contains(joined, "/var/run/docker.sock") || strings.Contains(joined, token) {
		t.Fatal("unsafe sandbox", joined)
	}
	if !strings.Contains(strings.Join(calls[2], " "), "label=forgelet.dev/runner-owner=our-device") || !reflect.DeepEqual(calls[3], []string{"rm", "-f", "-v", "our-container"}) {
		t.Fatal("cleanup did not verify ownership", calls)
	}
}

func TestActReadinessRequiresDiskCapacity(t *testing.T) {
	for _, tc := range []struct {
		name, available string
		ready           bool
	}{
		{"enough", "33554432", true}, {"exact budget", "25165824", true}, {"insufficient", "9437184", false}, {"unknown", "", false}, {"malformed", "overlay failed", false}, {"negative", "-1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, arch := range []string{"arm64", "amd64"} {
				for _, prefix := range []string{"forgelet-runner-linux-", "forgelet-act-linux-"} {
					if err := os.WriteFile(filepath.Join(dir, prefix+arch), []byte("worker"), 0700); err != nil {
						t.Fatal(err)
					}
				}
			}
			d := &dockerExecutor{binaryDir: dir, command: func(ctx context.Context, args ...string) *exec.Cmd {
				if args[0] == "image" {
					return exec.CommandContext(ctx, "/usr/bin/true")
				}
				return exec.CommandContext(ctx, "/usr/bin/printf", "%s", tc.available)
			}}
			var message string
			got := d.checkAct(t.Context(), run.RunnerContainer{DiskBytes: 24 << 30}, func(s Status) { message = s.Message })
			if got != tc.ready {
				t.Fatalf("ready=%v want=%v message=%s", got, tc.ready, message)
			}
			if tc.name == "insufficient" && !strings.Contains(message, "空间不足") {
				t.Fatal("missing capacity diagnostic", message)
			}
		})
	}
}
