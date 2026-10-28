//go:build integration

package integration_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This tests real Go Testcontainers processes against the development Docker
// daemon. The production on-demand daemon is checked separately by the original
// repository's PG18 workflow. No host socket is mounted into an execution Pod.
func TestToolsReaperReconnectGrace(t *testing.T) {
	image := os.Getenv("FORGELET_TEST_REAPER_IMAGE")
	alpine := os.Getenv("FORGELET_TEST_REAPER_ALPINE")
	if image == "" || alpine == "" {
		t.Skip("set FORGELET_TEST_REAPER_IMAGE and FORGELET_TEST_REAPER_ALPINE for the optional tool policy regression")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "docker", "image", "inspect", image, "--format", "{{json .Config.Env}}").CombinedOutput()
	must(t, err)
	var env []string
	must(t, json.Unmarshal(raw, &env))
	grace := ""
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "RYUK_RECONNECTION_TIMEOUT="); ok {
			grace = value
		}
	}
	if grace != "2m" {
		t.Fatalf("actual tool image Ryuk reconnect grace=%q want=2m", grace)
	}
	directory := t.TempDir()
	for _, name := range []string{"go.mod", "go.sum"} {
		b, err := os.ReadFile(filepath.Join("..", "..", name))
		must(t, err)
		must(t, os.WriteFile(filepath.Join(directory, name), b, 0600))
	}
	fixture, err := os.ReadFile(filepath.Join("..", "fixtures", "reaper", "probe_test.go.txt"))
	must(t, err)
	must(t, os.WriteFile(filepath.Join(directory, "probe_test.go"), fixture, 0600))
	binary := filepath.Join(directory, "reaper-probe.test")
	build := exec.CommandContext(ctx, "go", "test", "-mod=readonly", "-c", "-o", binary, ".")
	build.Dir = directory
	output, err := build.CombinedOutput()
	if err != nil {
		t.Fatalf("build reaper fixture: %v: %s", err, output)
	}
	for _, scenario := range []struct {
		name, grace, expected string
	}{
		{"standard_ten_second_gap", "10s", "false"},
		{"tool_image_two_minute_grace", grace, "true"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			state := filepath.Join(t.TempDir(), "state.json")
			t.Cleanup(func() { cleanupReaperProbe(t, ctx, state) })
			// Both test processes share this shell parent, as separate packages
			// share their go-test parent. The second waits beyond the old grace.
			command := exec.CommandContext(ctx, "sh", "-ec",
				"REAPER_PROBE_PHASE=first \"$1\" -test.v -test.timeout=50s\nREAPER_PROBE_PHASE=second \"$1\" -test.v -test.timeout=50s\nprintf 'two process probe finished\\n'\n",
				"reaper-probe", binary)
			command.Dir = directory
			command.Env = append(os.Environ(),
				"REAPER_PROBE_STATE="+state, "REAPER_PROBE_ALPINE="+alpine,
				"REAPER_EXPECT_REUSE="+scenario.expected, "RYUK_RECONNECTION_TIMEOUT="+scenario.grace)
			output, err := command.CombinedOutput()
			t.Log(string(output))
			must(t, err)
			if !strings.Contains(string(output), "reused="+scenario.expected) {
				t.Fatal("actual reaper lifecycle observation missing")
			}
		})
	}
}

func cleanupReaperProbe(t *testing.T, parent context.Context, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return
	}
	must(t, err)
	var state struct{ First, Second string }
	must(t, json.Unmarshal(raw, &state))
	seen := make(map[string]bool)
	for _, id := range []string{state.First, state.Second} {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		decoded, err := hex.DecodeString(id)
		if err != nil || len(decoded) != 32 {
			t.Fatalf("invalid tracked reaper ID %q", id)
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 15*time.Second)
		output, err := exec.CommandContext(ctx, "docker", "rm", "-f", id).CombinedOutput()
		cancel()
		if err != nil && !strings.Contains(string(output), "No such container") {
			t.Errorf("remove tracked test reaper: %v: %s", err, output)
		}
	}
}
