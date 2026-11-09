package sysinfo

import "testing"

func TestReadIsSafeOnThisPlatform(t *testing.T) {
	snapshot := Read()
	if snapshot.CPUs < 1 {
		t.Fatalf("processor count not reported: %+v", snapshot)
	}
	if snapshot.Load1 < 0 {
		t.Fatalf("negative load: %+v", snapshot)
	}
	if snapshot.MemoryTotal > 0 && snapshot.MemoryAvailable > snapshot.MemoryTotal {
		t.Fatalf("available memory above total: %+v", snapshot)
	}
}

// BenchmarkRead shows what a device spends reading its own numbers. The runner
// calls it once every 30 seconds, so this cost sits far below any poll or job.
func BenchmarkRead(b *testing.B) {
	for b.Loop() {
		Read()
	}
}
