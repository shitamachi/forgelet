package main

import (
	"testing"

	"github.com/shitamachi/forgelet/internal/server"
)

func TestDiskSpaceConfiguration(t *testing.T) {
	cfg, err := loadConfig(configFile(t, baseConfig))
	if err != nil || cfg.DiskSpace != server.DefaultDiskSpace() {
		t.Fatalf("defaults: %+v %v", cfg.DiskSpace, err)
	}
	for _, tc := range []struct {
		name, config string
		valid        bool
	}{
		{"custom", "{pause_below_bytes: 100, resume_at_bytes: 200}", true},
		{"partial", "{pause_below_bytes: 1024}", true},
		{"empty mapping", "{}", true},
		{"zero pause", "{pause_below_bytes: 0}", false},
		{"zero resume", "{resume_at_bytes: 0}", false},
		{"negative", "{pause_below_bytes: -1}", false},
		{"equal thresholds", "{pause_below_bytes: 100, resume_at_bytes: 100}", false},
		{"reversed thresholds", "{pause_below_bytes: 200, resume_at_bytes: 100}", false},
		{"float", "{pause_below_bytes: 1.5}", false},
		{"null field", "{pause_below_bytes: null}", false},
		{"string", "{pause_below_bytes: '100'}", false},
		{"overflow", "{resume_at_bytes: 18446744073709551616}", false},
		{"unknown field", "{resume_bytes: 1024}", false},
		{"null mapping", "null", false},
		{"sequence", "[]", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadConfig(configFile(t, baseConfig+"disk_space: "+tc.config+"\n"))
			if (err == nil) != tc.valid {
				t.Fatalf("config=%+v error=%v valid=%v", cfg.DiskSpace, err, tc.valid)
			}
			if tc.name == "custom" && cfg.DiskSpace != (server.DiskSpace{PauseBelowBytes: 100, ResumeAtBytes: 200}) {
				t.Fatalf("custom thresholds ignored: %+v", cfg.DiskSpace)
			}
		})
	}
}
