package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shitamachi/forgelet/internal/run"
)

// pollRunner is one poll from a device with the given capabilities and numbers.
func pollRunner(t *testing.T, h *harness, token, nonce, labels, platforms string, info *run.RunnerInfo) (int, []byte) {
	t.Helper()
	body, err := json.Marshal(runnerRequest{Attempt: "frja_" + strings.Repeat(nonce, 64), Info: info})
	if err != nil {
		t.Fatal(err)
	}
	return h.request("POST", "/runner/v1/poll", string(body), map[string]string{
		"Authorization":                "Bearer " + token,
		"Content-Type":                 "application/json",
		run.RunnerLabelsHeader:         labels,
		run.RunnerBuildPlatformsHeader: platforms,
	})
}

func deviceInfo(load float64) *run.RunnerInfo {
	return &run.RunnerInfo{OS: "windows", Arch: "amd64", CPUs: 8, MemoryTotalBytes: 16 << 30,
		MemoryAvailableBytes: 12 << 30, Load1: load, Backend: "wslc", Engine: "wslc 3.0.3.0"}
}

func TestBusyDeviceStepsAsideForIdlePeer(t *testing.T) {
	h := externalHarness(t, 2)
	busy, idle := pairDevice(t, h, "busy-wslc"), pairDevice(t, h, "idle-wslc")
	label := run.WSLCRunnerLabel

	// The idle device is online before any work exists.
	if code, body := pollRunner(t, h, idle, "a", label, "none", deviceInfo(0.05)); code != 204 {
		t.Fatalf("idle poll: %d %s", code, body)
	}
	item := h.manual(strings.Replace(testWorkflow, "runs-on: small", "runs-on: "+label, 1))

	// The loaded device must not take the job while the idle peer is ready.
	if code, body := pollRunner(t, h, busy, "b", label, "none", deviceInfo(7.5)); code != 204 || len(body) != 0 {
		t.Fatalf("busy poll took work: %d %s", code, body)
	}
	if job, err := h.store.GetJob(t.Context(), item.Jobs[0].ID); err != nil || job.State != run.Queued || job.RunnerID != "" {
		t.Fatalf("job left for the idle peer: %+v, %v", job, err)
	}
	// The idle device takes it on its next poll.
	if code, body := pollRunner(t, h, idle, "a", label, "none", deviceInfo(0.05)); code != 200 {
		t.Fatalf("idle poll: %d %s", code, body)
	}
	if job, err := h.store.GetJob(t.Context(), item.Jobs[0].ID); err != nil || job.RunnerID != "idle-wslc" || job.State != run.Starting {
		t.Fatalf("assignment: %+v, %v", job, err)
	}
}

func TestPausedPeerDoesNotHoldWork(t *testing.T) {
	h := externalHarness(t, 2)
	busy, idle := pairDevice(t, h, "busy-wslc"), pairDevice(t, h, "idle-wslc")
	label := run.WSLCRunnerLabel
	if code, body := pollRunner(t, h, idle, "a", label, "none", deviceInfo(0.05)); code != 204 {
		t.Fatalf("idle poll: %d %s", code, body)
	}
	if code, body := h.admin("PUT", "/api/runners/idle-wslc/pause", map[string]bool{"paused": true}); code != 200 {
		t.Fatalf("pause: %d %s", code, body)
	}
	h.manual(strings.Replace(testWorkflow, "runs-on: small", "runs-on: "+label, 1))
	if code, body := pollRunner(t, h, busy, "b", label, "none", deviceInfo(7.5)); code != 200 {
		t.Fatalf("busy device should take work when the peer is paused: %d %s", code, body)
	}
}

func TestPeerWithOtherPlatformsDoesNotHoldWork(t *testing.T) {
	h := externalHarness(t, 2)
	busy, idle := pairDevice(t, h, "busy-wslc"), pairDevice(t, h, "idle-wslc")
	label := run.WSLCRunnerLabel
	// The idle peer cannot build linux/amd64, so it is not a full substitute for
	// the busy device and must not hold its work back.
	if code, body := pollRunner(t, h, idle, "a", label, "linux/arm64", deviceInfo(0.05)); code != 204 {
		t.Fatalf("idle poll: %d %s", code, body)
	}
	h.manual(strings.Replace(testWorkflow, "runs-on: small", "runs-on: "+label, 1))
	if code, body := pollRunner(t, h, busy, "b", label, "linux/amd64", deviceInfo(7.5)); code != 200 {
		t.Fatalf("busy device was held back by an incompatible peer: %d %s", code, body)
	}
}

func TestRunnerReportSurvivesRestart(t *testing.T) {
	h := externalHarness(t, 2)
	token := pairDevice(t, h, "windows-wslc-01")
	if code, body := pollRunner(t, h, token, "a", run.WSLCRunnerLabel, "none", deviceInfo(0.3)); code != 204 {
		t.Fatalf("poll: %d %s", code, body)
	}
	h.http.Close()
	h.start()
	code, body := h.admin("GET", "/api/runners", nil)
	if code != 200 {
		t.Fatalf("runners: %d %s", code, body)
	}
	var views []runnerView
	if err := json.Unmarshal(body, &views); err != nil {
		t.Fatal(err)
	}
	for _, view := range views {
		if view.Name != "windows-wslc-01" {
			continue
		}
		if view.Status != "offline" || view.LastSeen.IsZero() {
			t.Fatalf("restarted view should be offline with a last-seen time: %+v", view)
		}
		if view.Info == nil || view.Info.CPUs != 8 || len(view.Labels) != 1 {
			t.Fatalf("persisted report: %+v", view)
		}
		return
	}
	t.Fatalf("device missing after restart: %s", body)
}

func TestBestOfSeveralIdleDevicesTakesTheJob(t *testing.T) {
	h := externalHarness(t, 2)
	label := run.WSLCRunnerLabel
	devices := []struct {
		name  string
		token string
		nonce string
		load  float64
	}{
		{"loaded-wslc", pairDevice(t, h, "loaded-wslc"), "a", 7.5},
		{"mid-wslc", pairDevice(t, h, "mid-wslc"), "b", 3.0},
		{"best-wslc", pairDevice(t, h, "best-wslc"), "c", 0.2},
	}
	for _, device := range devices {
		if code, body := pollRunner(t, h, device.token, device.nonce, label, "none", deviceInfo(device.load)); code != 204 {
			t.Fatalf("idle poll %s: %d %s", device.name, code, body)
		}
	}
	item := h.manual(strings.Replace(testWorkflow, "runs-on: small", "runs-on: "+label, 1))
	for _, device := range []int{1, 0} {
		if code, body := pollRunner(t, h, devices[device].token, devices[device].nonce, label, "none", deviceInfo(devices[device].load)); code != 204 {
			t.Fatalf("%s took the job before the best device: %d %s", devices[device].name, code, body)
		}
	}
	if code, body := pollRunner(t, h, devices[2].token, devices[2].nonce, label, "none", deviceInfo(devices[2].load)); code != 200 {
		t.Fatalf("best device: %d %s", code, body)
	}
	if job, err := h.store.GetJob(t.Context(), item.Jobs[0].ID); err != nil || job.RunnerID != "best-wslc" {
		t.Fatalf("assignment: %+v, %v", job, err)
	}
}

func TestRunnerViewReportsInfoJobAndDuration(t *testing.T) {
	h := externalHarness(t, 2)
	token := pairDevice(t, h, "windows-wslc-01")
	label := run.WSLCRunnerLabel
	item := h.manual(strings.Replace(testWorkflow, "runs-on: small", "runs-on: "+label, 1))
	if code, body := pollRunner(t, h, token, "a", label, "none", deviceInfo(0.4)); code != 200 {
		t.Fatalf("poll: %d %s", code, body)
	}
	if job, err := h.store.GetJob(t.Context(), item.Jobs[0].ID); err != nil || job.State != run.Starting || job.RunnerID != "windows-wslc-01" {
		t.Fatalf("reservation: %+v, %v", job, err)
	}
	code, body := h.admin("GET", "/api/runners", nil)
	if code != 200 {
		t.Fatalf("runners: %d %s", code, body)
	}
	var views []runnerView
	if err := json.Unmarshal(body, &views); err != nil {
		t.Fatal(err)
	}
	var view runnerView
	for _, candidate := range views {
		if candidate.Name == "windows-wslc-01" {
			view = candidate
		}
	}
	if view.Name == "" {
		t.Fatalf("views: %s", body)
	}
	if view.Status != "busy" || view.JobName != "Shell CI / test" || view.Repository != "me/repo" {
		t.Fatalf("job view: %+v", view)
	}
	if view.Info == nil || view.Info.CPUs != 8 || view.Info.Backend != "wslc" || view.Info.Engine != "wslc 3.0.3.0" {
		t.Fatalf("info: %+v", view.Info)
	}
	if len(view.Labels) != 1 || view.Labels[0] != label {
		t.Fatalf("labels: %v", view.Labels)
	}
	if view.StartedAt.IsZero() {
		t.Fatalf("timing: %+v", view)
	}
}
