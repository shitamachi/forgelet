package actworker

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestWorkerRejectsNegativeCPUBeforeStartingJob(t *testing.T) {
	request := run.ActRequest{CPUCount: -1, Image: "runner:test", Workspace: "/workspace", Plan: run.Plan{Kernel: "act", JobID: "job"}}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := Run(t.Context(), bytes.NewReader(data), io.Discard); err == nil || !strings.Contains(err.Error(), "invalid act job identity or profile") {
		t.Fatalf("negative CPU budget was not rejected before execution: %v", err)
	}
}

func TestWorkerProtocolPreservesExitCodesAndDecodedMasks(t *testing.T) {
	for _, tc := range []struct {
		name, stage, text, command, arg string
		code                            int
		mask                            string
	}{
		{name: "ordinary failure", stage: "Main", text: "exitcode '7': failure", code: 7},
		{name: "post failure is not main exit", stage: "Post", text: "exitcode '8': failure", code: -1},
		{name: "decoded secret", command: "add-mask", arg: "private-\"quoted\"\nvalue", code: -1, mask: "private-\"quoted\"\nvalue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := (formatter{}).Format(&logrus.Entry{Message: tc.text, Data: logrus.Fields{"stage": tc.stage, "command": tc.command, "arg": tc.arg, "stepID": []string{"outer", "inner"}}})
			if err != nil {
				t.Fatal(err)
			}
			var m run.ActMessage
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatal(err)
			}
			if m.StepID != "outer" || m.Mask != tc.mask {
				t.Fatal(m)
			}
			if tc.code < 0 {
				if m.ExitCode != nil {
					t.Fatal(m)
				}
			} else if m.ExitCode == nil || *m.ExitCode != tc.code {
				t.Fatal(m)
			}
			if tc.mask != "" && m.Message != "" {
				t.Fatal("mask command became log output", m)
			}
		})
	}
}
