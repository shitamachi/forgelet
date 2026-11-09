package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/blob"
	"github.com/shitamachi/forgelet/internal/run"
	"github.com/shitamachi/forgelet/internal/runner"
)

const macProfile = "macos-arm64"

var testMachineToken = "frr_" + strings.Repeat("a", 64)

func externalHarness(t *testing.T, slots int) *harness {
	t.Helper()
	h := newHarness(t, slots)
	h.http.Close()
	// A paired device with a known credential; tests that need more devices
	// pair them through the API.
	registration, err := json.Marshal(map[string]runnerRegistration{
		macProfile: {Digest: fmt.Sprintf("%x", sha256.Sum256([]byte(testMachineToken)))},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.SaveRunnerRegistrations(t.Context(), registration); err != nil {
		t.Fatal(err)
	}
	h.cfg.Blobs, err = blob.Open(filepath.Join(t.TempDir(), "artifacts"), blob.Limits{ObjectBytes: 1 << 20, TotalBytes: 4 << 20, Objects: 20})
	if err != nil {
		t.Fatal(err)
	}
	h.start()
	return h
}

// startPairing asks for a code the way a desktop app does.
func startPairing(t *testing.T, h *harness, hostname string) (id, secret string) {
	t.Helper()
	h.server.pairInterval = -1
	code, body := h.request("POST", "/runner/v1/pair",
		`{"hostname":"`+hostname+`","os":"windows","arch":"amd64","version":"test"}`, nil)
	var pairing struct{ ID, Code, Secret string }
	if err := json.Unmarshal(body, &pairing); code != 200 || err != nil || pairing.Code == "" || pairing.Secret == "" {
		t.Fatalf("pair %s: %d %s", hostname, code, body)
	}
	return pairing.ID, pairing.Secret
}

// collectPairing is the device polling for its credential after approval.
func collectPairing(t *testing.T, h *harness, id, secret string) (int, string) {
	t.Helper()
	code, body := h.request("GET", "/runner/v1/pair/"+id, "",
		map[string]string{"Authorization": "Bearer " + secret})
	var issued struct{ Token string }
	_ = json.Unmarshal(body, &issued)
	return code, issued.Token
}

// pairDevice runs the whole onboarding handshake: the device asks for a code,
// an administrator approves it, and the device collects its credential.
func pairDevice(t *testing.T, h *harness, name string) string {
	t.Helper()
	id, secret := startPairing(t, h, name)
	if approve, response := h.admin("POST", "/api/runner-pairings/"+id+"/approve", map[string]string{"name": name}); approve != 200 {
		t.Fatalf("approve %s: %d %s", name, approve, response)
	}
	code, token := collectPairing(t, h, id, secret)
	if code != 200 || !strings.HasPrefix(token, "frr_") {
		t.Fatalf("collect credential for %s: %d %s", name, code, token)
	}
	return token
}

func TestRunnerPollRejectsUnusableCapabilities(t *testing.T) {
	h := externalHarness(t, 1)
	attempt := "frja_" + strings.Repeat("b", 64)
	code, body := h.request("POST", "/runner/v1/poll", `{"attempt":"`+attempt+`"}`, map[string]string{
		"Authorization": "Bearer " + testMachineToken, "Content-Type": "application/json",
		run.RunnerLabelsHeader: "MacOS",
	})
	if code != 400 {
		t.Fatalf("invalid labels header: %d %s", code, body)
	}
}

func TestExternalRunnerExecutesNativeJobAndSharesPodCapacity(t *testing.T) {
	h := externalHarness(t, 1)
	item := h.manual(`on: push
jobs:
  mac:
    runs-on: macos-arm64
    steps:
      - run: echo local-executor > output.txt && cat output.txt
      - uses: actions/upload-artifact@v4
        with:
          name: mac-output
          path: output.txt
`)
	pod := h.manual(testWorkflow).Jobs[0]
	h.tick()
	mac := item.Jobs[0]
	if job, _ := h.store.GetJob(t.Context(), mac.ID); job.State != run.Queued || h.runtime.created != 1 {
		t.Fatalf("offline runner should stay queued: %+v, pods=%d", job, h.runtime.created)
	}
	attempt := "frja_" + strings.Repeat("b", 64)
	if code, body := h.externalRequest("/poll", testMachineToken, runnerRequest{Attempt: attempt}); code != 204 {
		t.Fatalf("Pod already uses the only slot: %d %s", code, body)
	}
	if code, body := h.runtimeRequest("POST", "claim", pod, nil); code != 200 {
		t.Fatal(code, string(body))
	}
	if code, body := h.runtimeRequest("POST", "result", pod, run.Result{State: run.Succeeded}); code != 200 {
		t.Fatal(code, string(body))
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var last runner.Status
	var desktopLog strings.Builder
	err := runner.Run(ctx, runner.Config{URL: h.http.URL, Token: testMachineToken}, t.TempDir(), func(status runner.Status) {
		if status.Log != nil {
			desktopLog.WriteString(status.Log.Data)
			return
		}
		last = status
		if status.State == "succeeded" || status.State == "failed" {
			cancel()
		}
	})
	if err != nil || last.State != "succeeded" || last.Job.JobID != mac.ID {
		t.Fatalf("runner result: %+v, %v", last, err)
	}
	if desktopLog.String() != "local-executor\n" {
		t.Fatalf("desktop log: %q", desktopLog.String())
	}
	job, err := h.store.GetJob(t.Context(), mac.ID)
	if err != nil || job.State != run.Succeeded || job.PodUID != "" || job.AttemptID == "" || job.Result.LastLogSeq == 0 {
		t.Fatalf("Mac execution identity/result: %+v, %v", job, err)
	}
	logs, err := h.store.Logs(t.Context(), mac.ID, 0)
	if err != nil || !strings.Contains(fmt.Sprint(logs), "local-executor") {
		t.Fatalf("logs: %v %v", logs, err)
	}
	code, body := h.admin("GET", "/api/runs/"+item.ID+"/artifacts", nil)
	if code != 200 || !strings.Contains(string(body), "mac-output") {
		t.Fatalf("artifact: %d %s", code, body)
	}
	h.tick()
	if h.runtime.created != 1 {
		t.Fatal("external job also created a Pod")
	}
}

func TestExternalRunnerGrantFencingAndCancellation(t *testing.T) {
	h := externalHarness(t, 2)
	h.http.Close()
	otherToken := "frr_" + strings.Repeat("b", 64)
	registrations, err := json.Marshal(map[string]runnerRegistration{
		macProfile:  {Digest: fmt.Sprintf("%x", sha256.Sum256([]byte(testMachineToken)))},
		"other-mac": {Digest: fmt.Sprintf("%x", sha256.Sum256([]byte(otherToken)))},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.SaveRunnerRegistrations(t.Context(), registrations); err != nil {
		t.Fatal(err)
	}
	h.start()
	item := h.manual(strings.ReplaceAll(testWorkflow, "runs-on: small", "runs-on: macos-arm64"))
	input := runnerRequest{Attempt: "frja_" + strings.Repeat("b", 64), JobID: item.Jobs[0].ID}
	check := func(endpoint, token string, body any, want int) {
		t.Helper()
		if code, data := h.externalRequest(endpoint, token, body); code != want {
			t.Fatalf("%s: %d want %d: %s", endpoint, code, want, data)
		}
	}
	check("/poll", input.Attempt, input, 401)
	check("/poll", testMachineToken, input, 200)
	check("/poll", otherToken, input, 409)
	check("/renew", otherToken, input, 403)
	h.http.Close()
	h.start()
	check("/poll", testMachineToken, input, 200)
	check("/poll", testMachineToken, runnerRequest{Attempt: "frja_" + strings.Repeat("c", 64)}, 204)

	jobPath := "/jobs/" + input.JobID
	check(jobPath+"/claim", testMachineToken, nil, 401)
	check(jobPath+"/claim", input.Attempt, nil, 200)
	if code, _ := h.request("POST", "/internal/jobs/"+input.JobID+"/claim", "", map[string]string{"Authorization": "Bearer " + input.Attempt}); code != 401 {
		t.Fatalf("attempt token reached Pod route: %d", code)
	}
	check("/renew", testMachineToken, input, 200)
	code, _ := h.request("GET", "/runner/v1"+jobPath+"/blobs/cache?key=unsafe", "", map[string]string{"Authorization": "Bearer " + input.Attempt})
	if code != 403 {
		t.Fatalf("external cache access: %d", code)
	}
	if code, _ := h.admin("POST", "/api/runs/"+item.ID+"/cancel", nil); code != 200 {
		t.Fatal(code)
	}
	check(jobPath+"/result", input.Attempt, run.Result{State: run.Succeeded}, 409)
	check(jobPath+"/result", input.Attempt, run.Result{State: run.Cancelled}, 200)
	check(jobPath+"/result", input.Attempt, run.Result{State: run.Cancelled}, 200)
	check("/poll", testMachineToken, input, 409)

	next := h.manual(strings.ReplaceAll(testWorkflow, "runs-on: small", "runs-on: macos-arm64"))
	input.Attempt = "frja_" + strings.Repeat("d", 64)
	input.JobID = next.Jobs[0].ID
	check("/poll", testMachineToken, input, 200)
	if err := h.store.ExpireExternal(t.Context(), input.JobID, time.Now().Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	job, err := h.store.GetJob(t.Context(), input.JobID)
	if err != nil || job.State != run.Interrupted {
		t.Fatalf("lost grant was not interrupted: %+v %v", job, err)
	}
	check("/jobs/"+input.JobID+"/claim", input.Attempt, nil, 409)
}

func TestRunnerCredentialExecutesJobsAcrossRepositories(t *testing.T) {
	h := externalHarness(t, 1)
	h.http.Close()
	if err := h.store.SaveRunnerRegistrations(t.Context(), []byte("{}")); err != nil {
		t.Fatal(err)
	}
	h.cfg.Repositories = []string{"me/repo", "me/another", "me/disabled"}
	h.start()
	const device = "lab-mac"
	credential := pairDevice(t, h, device)

	create := func(repository, label string) run.Run {
		t.Helper()
		source := strings.ReplaceAll(testWorkflow, "runs-on: small", "runs-on: "+label)
		code, body := h.admin("POST", "/api/runs", map[string]any{
			"repository": repository, "sha": testSHA, "ref": "refs/heads/main", "workflow": source,
		})
		var item run.Run
		if err := json.Unmarshal(body, &item); code != 201 || err != nil {
			t.Fatalf("create %s: %d %s", repository, code, body)
		}
		return item
	}
	disabled := create("me/disabled", run.MacRunnerLabel)
	first := create("me/repo", run.MacRunnerLabel)
	second := create("me/another", run.WSLCRunnerLabel)
	h.http.Close()
	h.cfg.Repositories = h.cfg.Repositories[:2]
	h.start()

	ctx, stop := context.WithTimeout(t.Context(), 10*time.Second)
	defer stop()
	var repositories []string
	err := runner.Run(ctx, runner.Config{URL: h.http.URL, Token: credential}, t.TempDir(), func(status runner.Status) {
		if status.State == "failed" {
			stop()
		}
		if status.State == "succeeded" {
			repositories = append(repositories, status.Job.Repository)
			if len(repositories) == 2 {
				stop()
			}
		}
	})
	if err != nil || strings.Join(repositories, ",") != "me/repo,me/another" {
		t.Fatalf("same credential executed %v: %v", repositories, err)
	}
	for _, item := range []run.Run{first, second} {
		job, err := h.store.GetJob(t.Context(), item.Jobs[0].ID)
		if err != nil || job.RunnerID != device || job.PodUID != "" || job.State != run.Succeeded {
			t.Fatalf("external assignment: %+v, %v", job, err)
		}
	}
	job, err := h.store.GetJob(t.Context(), disabled.Jobs[0].ID)
	if err != nil || job.State != run.Queued || job.RunnerID != "" {
		t.Fatalf("disabled repository assignment: %+v, %v", job, err)
	}
}

func TestExternalWorkflowAdmission(t *testing.T) {
	h := externalHarness(t, 2)
	base := strings.ReplaceAll(testWorkflow, "runs-on: small", "runs-on: macos-arm64")
	for _, tc := range []struct {
		name, ref, source string
	}{
		{"tag", "refs/tags/v1.0.0", base},
		{"secrets", "refs/heads/main", strings.Replace(base, "echo hello", "echo ${{ secrets.PRIVATE }}", 1)},
		{"mixed with Pods", "refs/heads/main", base + "  pod:\n    runs-on: small\n    steps:\n      - run: echo pod\n"},
		{"cache", "refs/heads/main", base + "      - uses: actions/cache@v4\n        with: {path: out, key: unsafe}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := h.admin("POST", "/api/runs", map[string]any{"repository": "me/repo", "sha": testSHA, "ref": tc.ref, "workflow": tc.source})
			if code != 422 {
				t.Fatalf("admission: %d %s", code, body)
			}
		})
	}
}

func (h *harness) externalRequest(endpoint, token string, body any) (int, []byte) {
	h.t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		h.t.Fatal(err)
	}
	return h.request("POST", "/runner/v1"+endpoint, string(data), map[string]string{
		"Authorization": "Bearer " + token, "Content-Type": "application/json",
	})
}

func TestExternalRunnerStoppingSessionCancelsActiveJob(t *testing.T) {
	h := externalHarness(t, 1)
	item := h.manual(`on: push
jobs:
  mac:
    runs-on: macos-arm64
    steps:
      - run: |
          echo ready-to-stop
          sleep 120
`)
	ctx, stop := context.WithTimeout(t.Context(), 10*time.Second)
	defer stop()
	done := make(chan error, 1)
	workRoot := t.TempDir()
	go func() {
		done <- runner.Run(ctx, runner.Config{URL: h.http.URL, Token: testMachineToken}, workRoot, func(runner.Status) {})
	}()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		logs, err := h.store.Logs(t.Context(), item.Jobs[0].ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(fmt.Sprint(logs), "ready-to-stop") {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("job never reached its shell step")
		case <-ticker.C:
		}
	}
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not stop its active job")
	}
	job, err := h.store.GetJob(t.Context(), item.Jobs[0].ID)
	if err != nil || job.State != run.Cancelled || job.Result.LastLogSeq == 0 {
		t.Fatalf("stopped job: %+v, %v", job, err)
	}
}
