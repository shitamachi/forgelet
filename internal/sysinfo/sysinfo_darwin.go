//go:build darwin

package sysinfo

import (
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Read uses two sysctl calls. macOS exposes the load average as a formatted
// string; free memory needs per-pager statistics, which is more work than this
// report is worth, so memory pressure stays unknown there.
func Read() Snapshot {
	snapshot := Snapshot{CPUs: cpus()}
	if load, err := unix.Sysctl("vm.loadavg"); err == nil {
		fields := strings.Fields(strings.Trim(load, "{}"))
		if len(fields) > 0 {
			if value, err := strconv.ParseFloat(fields[0], 64); err == nil {
				snapshot.Load1 = value
			}
		}
	}
	if total, err := unix.SysctlUint64("hw.memsize"); err == nil {
		snapshot.MemoryTotal = total
	}
	return snapshot
}
