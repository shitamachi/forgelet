package server

import (
	"strings"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestDeleteRunnerRemovesDevice(t *testing.T) {
	h := externalHarness(t, 2)
	token := pairDevice(t, h, "spare-wslc")
	if code, body := pollRunner(t, h, token, "a", run.WSLCRunnerLabel, "none", deviceInfo(0.1)); code != 204 {
		t.Fatalf("poll: %d %s", code, body)
	}
	if code, body := h.admin("DELETE", "/api/runners/spare-wslc", nil); code != 200 {
		t.Fatalf("delete: %d %s", code, body)
	}
	// The credential stops working and the device leaves the inventory.
	if code, body := pollRunner(t, h, token, "a", run.WSLCRunnerLabel, "none", deviceInfo(0.1)); code != 401 {
		t.Fatalf("deleted credential still polls: %d %s", code, body)
	}
	code, body := h.admin("GET", "/api/runners", nil)
	if code != 200 || strings.Contains(string(body), "spare-wslc") {
		t.Fatalf("inventory after delete: %d %s", code, body)
	}
	if code, _ := h.admin("DELETE", "/api/runners/spare-wslc", nil); code != 404 {
		t.Fatalf("second delete: %d", code)
	}
}

func TestDeleteRunnerWaitsForActiveLease(t *testing.T) {
	h := externalHarness(t, 2)
	token := pairDevice(t, h, "busy-wslc")
	item := h.manual(strings.Replace(testWorkflow, "runs-on: small", "runs-on: "+run.WSLCRunnerLabel, 1))
	if code, body := pollRunner(t, h, token, "a", run.WSLCRunnerLabel, "none", deviceInfo(0.1)); code != 200 {
		t.Fatalf("poll: %d %s", code, body)
	}
	if code, body := h.admin("DELETE", "/api/runners/busy-wslc", nil); code != 409 {
		t.Fatalf("delete with a live lease: %d %s", code, body)
	}
	// Once the attempt is over the device can be removed.
	if err := h.store.ExpireExternal(t.Context(), item.Jobs[0].ID, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if code, body := h.admin("DELETE", "/api/runners/busy-wslc", nil); code != 200 {
		t.Fatalf("delete after the lease: %d %s", code, body)
	}
}
