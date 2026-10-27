package executor

import (
	"fmt"
	"os"
	"strings"
)

func prepareEngineCgroup(root string) error {
	controllers, err := os.ReadFile(root + "/cgroup.controllers")
	if err != nil {
		return fmt.Errorf("read act engine cgroup v2 controllers: %w", err)
	}
	available := " " + strings.Join(strings.Fields(string(controllers)), " ") + " "
	for _, name := range []string{"cpu", "memory", "pids"} {
		if !strings.Contains(available, " "+name+" ") {
			return fmt.Errorf("act engine requires cgroup v2 %s controller", name)
		}
	}
	if err := os.MkdirAll(root+"/init", 0755); err != nil {
		return fmt.Errorf("prepare act engine process cgroup: %w", err)
	}
	procs, err := os.ReadFile(root + "/cgroup.procs")
	if err != nil {
		return fmt.Errorf("read act engine processes: %w", err)
	}
	for _, pid := range strings.Fields(string(procs)) {
		if err := os.WriteFile(root+"/init/cgroup.procs", []byte(pid), 0600); err != nil {
			return fmt.Errorf("delegate act engine process %s: %w", pid, err)
		}
	}
	if err := os.WriteFile(root+"/cgroup.subtree_control", []byte("+cpu +memory +pids"), 0600); err != nil {
		return fmt.Errorf("enable bounded act engine child cgroups: %w", err)
	}
	return nil
}
