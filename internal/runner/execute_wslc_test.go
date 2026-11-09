//go:build windows

package runner

import (
	"testing"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestJobTerminalAcceptsBothVocabularies(t *testing.T) {
	for _, tc := range []struct {
		state string
		want  bool
	}{
		{"success", true},
		{"failure", true},
		{"cancelled", true},
		{"timed_out", true},
		{"interrupted", true},
		{"succeeded", true},
		{"failed", true},
		{"stopped", true},
		{"running", false},
		{"queued", false},
		{"", false},
	} {
		if got := jobTerminal(tc.state); got != tc.want {
			t.Fatalf("jobTerminal(%q) = %v, want %v", tc.state, got, tc.want)
		}
	}
}

func TestDesktopStateNamesTheResult(t *testing.T) {
	for _, tc := range []struct {
		state run.State
		want  string
	}{
		{run.Succeeded, "succeeded"},
		{run.Failed, "failed"},
		{run.Cancelled, "stopped"},
		{run.Interrupted, "stopped"},
		{run.TimedOut, "timed_out"},
		{run.Running, "running"},
	} {
		if got := DesktopState(tc.state); got != tc.want {
			t.Fatalf("DesktopState(%q) = %q, want %q", tc.state, got, tc.want)
		}
	}
}

func TestTailWriterReportsOnlyMeaningfulStderr(t *testing.T) {
	warning := &tailWriter{limit: 4096}
	_, _ = warning.Write([]byte("wsl: Your kernel does not support swap limit capabilities or the cgroup is not mounted. Memory limited without swap.\n"))
	if got := warning.Meaningful(); got != "" {
		t.Fatalf("swap warning surfaced as %q", got)
	}
	failure := &tailWriter{limit: 4096}
	_, _ = failure.Write([]byte("wsl: Your kernel does not support swap limit capabilities.\nno such image: runner\n"))
	if got := failure.Meaningful(); got != "no such image: runner" {
		t.Fatalf("Meaningful() = %q", got)
	}
}
