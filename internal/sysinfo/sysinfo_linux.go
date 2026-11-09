//go:build linux

package sysinfo

import (
	"os"
	"strconv"
	"strings"
)

// Read parses /proc: a single page of kernel-maintained text, no subprocesses.
func Read() Snapshot {
	snapshot := Snapshot{CPUs: cpus()}
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) > 0 {
			if value, err := strconv.ParseFloat(fields[0], 64); err == nil {
				snapshot.Load1 = value
			}
		}
	}
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		for line := range strings.Lines(string(data)) {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			value, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				continue
			}
			switch strings.TrimSuffix(fields[0], ":") {
			case "MemTotal":
				snapshot.MemoryTotal = value << 10
			case "MemAvailable":
				snapshot.MemoryAvailable = value << 10
			}
		}
		if snapshot.MemoryAvailable == 0 {
			snapshot.MemoryAvailable = snapshot.MemoryTotal
		}
	}
	return snapshot
}
