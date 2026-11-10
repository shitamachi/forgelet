//go:build windows

package runner

import "testing"

func TestDeviceLabelsFollowTheBackend(t *testing.T) {
	if labels := deviceLabels(Config{WSLC: nil}); len(labels) != 1 || labels[0] != "linux-amd64-wslc" {
		t.Fatalf("wslc labels = %v", labels)
	}
	if labels := deviceLabels(Config{WSL2: &WSL2Config{}}); len(labels) != 1 || labels[0] != "linux-amd64-wsl2" {
		t.Fatalf("wsl2 labels = %v", labels)
	}
}

func TestWindowsBackendRequiresAChoice(t *testing.T) {
	if _, err := newBackend(t.Context(), Config{}, func(Status) {}); err == nil {
		t.Fatal("a device without a backend was accepted")
	}
}
