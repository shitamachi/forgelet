package run

import (
	"math"
	"strings"
	"testing"
)

func TestRunnerLabels(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
		want   []string
	}{
		{"absent", "", nil},
		{"mac", MacRunnerLabel, []string{MacRunnerLabel}},
		{"both", "linux-amd64-wslc,macos-arm64", []string{"linux-amd64-wslc", "macos-arm64"}},
		{"portable", "portable-build", []string{"portable-build"}},
		{"duplicates", "macos-arm64,macos-arm64", []string{MacRunnerLabel}},
		{"invalid", "macos-arm64,Bad Label,", []string{MacRunnerLabel}},
		{"empty", ",", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := RunnerLabels(tc.header)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestDeviceLabelMismatchKeepsPortableLabelsShared(t *testing.T) {
	for _, tc := range []struct {
		name       string
		label      string
		caps       []string
		mismatch   bool
	}{
		{"mac device", MacRunnerLabel, []string{MacRunnerLabel}, false},
		{"wslc device", MacRunnerLabel, []string{WSLCRunnerLabel}, true},
		{"legacy client", MacRunnerLabel, nil, false},
		{"portable label", "portable-build", []string{WSLCRunnerLabel}, false},
		{"built-in on portable-only device", WSLCRunnerLabel, []string{"portable-build"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DeviceLabelMismatch(tc.label, tc.caps); got != tc.mismatch {
				t.Fatalf("DeviceLabelMismatch(%q, %v) = %v", tc.label, tc.caps, got)
			}
		})
	}
}

func TestRunnerInfoScore(t *testing.T) {
	for _, tc := range []struct {
		name string
		info RunnerInfo
		want float64
	}{
		{"idle", RunnerInfo{CPUs: 8, Load1: 0, MemoryTotalBytes: 16 << 30, MemoryAvailableBytes: 16 << 30}, 0},
		{"healthy memory use", RunnerInfo{CPUs: 8, Load1: 0, MemoryTotalBytes: 16 << 30, MemoryAvailableBytes: 12 << 30}, 0},
		{"half cpus", RunnerInfo{CPUs: 8, Load1: 4}, 0.5},
		{"saturated", RunnerInfo{CPUs: 4, Load1: 12}, 1},
		{"memory bound", RunnerInfo{CPUs: 8, Load1: 0.1, MemoryTotalBytes: 16 << 30, MemoryAvailableBytes: 2 << 30}, 0.8333333333333334},
		{"unknown memory", RunnerInfo{CPUs: 4, Load1: 1}, 0.25},
		{"no readings", RunnerInfo{}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.info.Score(); math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("Score() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRunnerInfoBounds(t *testing.T) {
	if !(RunnerInfo{OS: "windows", CPUs: 8, Load1: 2.5, MemoryTotalBytes: 8 << 30, MemoryAvailableBytes: 4 << 30}).ValidBounds() {
		t.Fatal("ordinary report rejected")
	}
	for name, info := range map[string]RunnerInfo{
		"cpu count":    {CPUs: 5000},
		"negative cpu": {CPUs: -1},
		"huge memory":  {MemoryTotalBytes: 1 << 51},
		"available above total": {MemoryTotalBytes: 1 << 30,
			MemoryAvailableBytes: 2 << 30},
		"negative load":      {Load1: -1},
		"oversized os name":  {OS: strings.Repeat("x", 40)},
		"oversized version": {Version: strings.Repeat("9", 80)},
	} {
		if info.ValidBounds() {
			t.Fatalf("%s accepted: %+v", name, info)
		}
	}
}

func TestValidRunnerLabel(t *testing.T) {
	for _, tc := range []struct {
		label string
		valid bool
	}{
		{"macos-arm64", true},
		{"linux-amd64-wslc", true},
		{"a", true},
		{"", false},
		{"-leading", false},
		{"Upper", false},
		{"with space", false},
		{"underscore_label", false},
	} {
		if got := ValidRunnerLabel(tc.label); got != tc.valid {
			t.Fatalf("ValidRunnerLabel(%q) = %v, want %v", tc.label, got, tc.valid)
		}
	}
}
