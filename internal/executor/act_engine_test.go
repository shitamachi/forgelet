package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestActEngineCgroupDelegation(t *testing.T) {
	for _, tc := range []struct{ name, controllers, processes, wantError string }{
		{"bounded children", "cpuset cpu io memory pids", "1\n42\n", ""},
		{"no processes", "cpu memory pids", "", ""},
		{"missing CPU", "memory pids", "1", "cpu controller"},
		{"missing memory", "cpu pids", "1", "memory controller"},
		{"missing pids", "cpu memory", "1", "pids controller"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for name, value := range map[string]string{"cgroup.controllers": tc.controllers, "cgroup.procs": tc.processes} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := prepareEngineCgroup(root)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(root, "init")); !os.IsNotExist(err) {
					t.Fatal("incomplete budget must fail before delegating")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(root, "cgroup.subtree_control"))
			if err != nil || string(data) != "+cpu +memory +pids" {
				t.Fatalf("child controllers: %q %v", data, err)
			}
			if tc.processes != "" {
				data, err = os.ReadFile(filepath.Join(root, "init/cgroup.procs"))
				fields := strings.Fields(tc.processes)
				if err != nil || string(data) != fields[len(fields)-1] {
					t.Fatalf("process delegation: %q %v", data, err)
				}
			}
		})
	}
}

func TestActEngineFailsWithoutCgroupOrAfterCancellation(t *testing.T) {
	if err := prepareEngineCgroup(t.TempDir()); err == nil {
		t.Fatal("accepted missing cgroup v2")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := RunActEngine(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
