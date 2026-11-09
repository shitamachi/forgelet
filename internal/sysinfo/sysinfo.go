// Package sysinfo reads the few operating-system numbers a device reports to
// the control plane. Every call is a handful of system calls or one small
// /proc read, so it can run while the machine is busy with other work.
package sysinfo

import "runtime"

// Snapshot is one reading. Zero values mean "unknown": a platform that cannot
// answer without extra work simply leaves the field out.
type Snapshot struct {
	CPUs            int
	MemoryTotal     uint64
	MemoryAvailable uint64
	Load1           float64
}

// cpus is the processor count every platform reports the same way.
func cpus() int { return runtime.NumCPU() }
