package runner

import (
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestChildKeepsAcknowledgedTerminalState(t *testing.T) {
	const child = "FORGELET_TEST_CHILD_TERMINAL_STATE"
	if os.Getenv(child) == "1" {
		fmt.Println(`{"state":"timed_out"}`)
		fmt.Println(`{"state":"error","message":"executor exited after reporting its result"}`)
		os.Exit(1)
	}
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestChildKeepsAcknowledgedTerminalState$")
	command.Env = append(command.Environ(), child+"=1")
	result := executeChild(t.Context(), command, ExecRequest{}, run.ExternalGrant{}, func(Status) {}, nil)
	if result.State != "timed_out" {
		t.Fatalf("terminal result was replaced by process failure: %+v", result)
	}
}
