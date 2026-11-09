//go:build windows

package sysinfo

import (
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32          = windows.NewLazySystemDLL("kernel32.dll")
	globalMemoryState = kernel32.NewProc("GlobalMemoryStatusEx")
	systemTimes       = kernel32.NewProc("GetSystemTimes")
)

// memoryStatus matches MEMORYSTATUSEX from the Windows SDK.
type memoryStatus struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

var cpu struct {
	sync.Mutex
	idle, total uint64
	primed      bool
}

// Read asks the kernel for memory and derives a one-core-equivalent load from
// two samples of its CPU times. It starts no process and reads no file.
func Read() Snapshot {
	snapshot := Snapshot{CPUs: cpus()}
	var memory memoryStatus
	memory.Length = uint32(unsafe.Sizeof(memory))
	if ok, _, _ := globalMemoryState.Call(uintptr(unsafe.Pointer(&memory))); ok != 0 {
		snapshot.MemoryTotal = memory.TotalPhys
		snapshot.MemoryAvailable = memory.AvailPhys
	}
	snapshot.Load1 = loadAverage(snapshot.CPUs)
	return snapshot
}

// loadAverage reports how many processors were busy since the previous call.
// Windows has no load average, so this is the closest equivalent the kernel
// exposes without extra work.
func loadAverage(cpuCount int) float64 {
	var idle, kernel, user windows.Filetime
	if ok, _, _ := systemTimes.Call(uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user))); ok == 0 {
		return 0
	}
	idleTicks := filetimeTicks(idle)
	totalTicks := filetimeTicks(kernel) + filetimeTicks(user)
	cpu.Lock()
	defer cpu.Unlock()
	previousIdle, previousTotal, primed := cpu.idle, cpu.total, cpu.primed
	cpu.idle, cpu.total, cpu.primed = idleTicks, totalTicks, true
	if !primed || totalTicks <= previousTotal || idleTicks < previousIdle {
		return 0
	}
	busy := 1 - float64(idleTicks-previousIdle)/float64(totalTicks-previousTotal)
	return max(0, min(busy, 1)) * float64(cpuCount)
}

func filetimeTicks(time windows.Filetime) uint64 {
	return uint64(time.HighDateTime)<<32 | uint64(time.LowDateTime)
}
