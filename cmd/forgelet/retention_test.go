package main

import (
	"testing"
	"time"
)

func TestRetentionConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, extra string
		valid       bool
	}{
		{"default", "", true},
		{"partial", "retention:\n  interval: 1h\n", true},
		{"short test policy", "retention:\n  history: 10s\n  logs: 2s\n  events: 10s\n  backups: 3s\n  interval: 1s\n", true},
		{"unknown", "retention:\n  typo: 1h\n", false},
		{"negative", "retention:\n  history: -1h\n", false},
		{"zero", "retention:\n  logs: 0s\n", false},
		{"missing unit", "retention:\n  history: 10\n", false},
		{"logs outlive history", "retention:\n  history: 1h\n", false},
		{"excessive", "retention:\n  history: 999999h\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadConfig(configFile(t, baseConfig+tc.extra))
			if (err == nil) != tc.valid {
				t.Fatalf("loadConfig error=%v, want valid=%v", err, tc.valid)
			}
			if err == nil && (tc.name == "default" || tc.name == "partial") {
				policy, err := cfg.Retention.policy()
				if err != nil || policy.History != 30*24*time.Hour || policy.Logs != 7*24*time.Hour {
					t.Fatalf("defaults lost: %+v, %v", policy, err)
				}
			}
		})
	}
}
