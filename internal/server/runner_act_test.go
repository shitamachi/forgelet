package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/kube"
	"github.com/shitamachi/forgelet/internal/run"
)

func TestActDeviceExecutesOriginalStepsAndUsesScopedCache(t *testing.T) {
	for _, tc := range []struct {
		label string
		ready bool
	}{{"ubuntu-latest", true}, {"ubuntu-latest", false}, {"ubuntu-24.04", true}, {"ubuntu-24.04", false}} {
		t.Run(tc.label+"/"+map[bool]string{true: "device", false: "pod"}[tc.ready], func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"auths":{}}`), 0600); err != nil {
				t.Fatal(err)
			}
			h := externalHarnessWith(t, 1, func(h *harness) {
				h.cfg.Profiles[tc.label] = kube.Profile{Image: "executor:act", Act: &kube.ActProfile{RunnerImage: "registry.test/tools@sha256:" + strings.Repeat("a", 64), Platform: "linux/amd64", CPULimit: "4", MemoryLimit: "4Gi"}}
				h.cfg.RunnerActImage = "registry.test/engine@sha256:" + strings.Repeat("b", 64)
				h.cfg.RunnerRegistryConfig = dir
				h.cfg.RunnerFallbacks = map[string]string{tc.label: tc.label}
				h.cfg.RunnerSecrets = []string{"me/repo"}
			})
			env, err := actRunnerContainer(h.cfg, h.cfg.Profiles[tc.label])
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal([]run.RunnerContainer{env})
			headers := map[string]string{"Authorization": "Bearer " + testMachineToken, "Content-Type": "application/json", run.RunnerLabelsHeader: run.MacRunnerLabel}
			if tc.ready {
				headers[run.RunnerContainersHeader] = string(encoded)
			}
			attempt := "frja_" + strings.Repeat("c", 64)
			poll := func() (int, []byte) {
				return h.request("POST", "/runner/v1/poll", `{"attempt":"`+attempt+`"}`, headers)
			}
			if code, b := poll(); code != 204 {
				t.Fatal(code, string(b))
			}
			item := h.manual(strings.ReplaceAll(`on: push
jobs:
  test:
    runs-on: ubuntu-latest
    timeout-minutes: 20
    steps:
      - uses: actions/setup-go@v5
        with: {go-version: '1.26.4'}
      - run: docker info
`, "ubuntu-latest", tc.label))
			if item.Jobs[0].Plan.TimeoutSeconds != 1200 {
				t.Fatal("lost original timeout")
			}
			h.tick()
			job := podScheduleRun(h, item.ID).Jobs[0]
			code, b := poll()
			if !tc.ready {
				if code != 204 || job.PodUID == "" || job.Plan.External {
					t.Fatalf("fallback: %d %s %+v", code, b, job)
				}
				profile, err := h.server.fallbackPodProfile(job.Plan)
				if err != nil || profile.Act.Platform != "linux/amd64" {
					t.Fatal(profile, err)
				}
				return
			}
			if code != 200 || job.PodUID != "" {
				t.Fatal(code, string(b), job)
			}
			endpoint := "/jobs/" + job.ID
			if code, b := h.externalRequest(endpoint+"/claim", attempt, nil); code != 200 {
				t.Fatal(code, string(b))
			}
			get := func(path string) (int, []byte) {
				return h.request(http.MethodGet, "/runner/v1"+endpoint+path, "", map[string]string{"Authorization": "Bearer " + attempt})
			}
			if code, b := get("/credentials"); code != 200 || !strings.Contains(string(b), `"registry_config":{"auths":{}}`) {
				t.Fatal(code, string(b))
			}
			if code, b := get("/blobs/cache?key=missing&version=" + strings.Repeat("a", 64)); code != 404 {
				t.Fatal("act cache denied", code, string(b))
			}
			h.server.cfg.RunnerSecrets = nil
			if code, b := get("/credentials"); code != 403 {
				t.Fatal("revoked authorization", code, string(b))
			}
		})
	}
}

func TestActRunnerHandoffKeepsDependentJobOffPod(t *testing.T) {
	for _, mode := range []string{"next poll", "lost runner", "paused runner", "duplicate result"} {
		t.Run(mode, func(t *testing.T) {
			h := externalHarnessWith(t, 1, func(h *harness) {
				h.cfg.Profiles["ubuntu-latest"] = kube.Profile{Image: "executor:act", Act: &kube.ActProfile{RunnerImage: "registry.test/tools@sha256:" + strings.Repeat("a", 64), Platform: "linux/amd64"}}
				h.cfg.RunnerActImage = "registry.test/engine@sha256:" + strings.Repeat("b", 64)
				h.cfg.RunnerRegistryConfig = t.TempDir()
				h.cfg.RunnerFallbacks = map[string]string{"ubuntu-latest": "ubuntu-latest"}
				h.cfg.RunnerSecrets = []string{"me/repo"}
			})
			now := time.Now().UTC()
			h.server.now = func() time.Time { return now }
			env, err := actRunnerContainer(h.cfg, h.cfg.Profiles["ubuntu-latest"])
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal([]run.RunnerContainer{env})
			headers := map[string]string{"Authorization": "Bearer " + testMachineToken, "Content-Type": "application/json", run.RunnerLabelsHeader: run.MacRunnerLabel, run.RunnerContainersHeader: string(encoded)}
			poll := func(nonce string) (int, []byte) {
				return h.request("POST", "/runner/v1/poll", `{"attempt":"frja_`+strings.Repeat(nonce, 64)+`"}`, headers)
			}
			if c, b := poll("c"); c != 204 {
				t.Fatal(c, string(b))
			}
			item := h.manual(`on: push
jobs:
  first:
    runs-on: ubuntu-latest
    steps: [{run: echo first}]
  second:
    needs: first
    runs-on: ubuntu-latest
    steps: [{run: echo second}]
`)
			h.tick()
			c, b := poll("c")
			var grant run.ExternalGrant
			if c != 200 || json.Unmarshal(b, &grant) != nil {
				t.Fatal(c, string(b))
			}
			token := "frja_" + strings.Repeat("c", 64)
			endpoint := "/jobs/" + grant.JobID
			if c, b := h.externalRequest(endpoint+"/claim", token, nil); c != 200 {
				t.Fatal(c, string(b))
			}
			// The old idle announcement is gone, as it is after a long real build.
			now = now.Add(30 * time.Second)
			if c, b := h.externalRequest(endpoint+"/result", token, run.Result{State: run.Succeeded}); c != 200 {
				t.Fatal(c, string(b))
			}
			if mode == "paused runner" {
				if c, b := h.admin("PUT", "/api/runners/"+macProfile+"/pause", map[string]bool{"paused": true}); c != 200 {
					t.Fatal(c, string(b))
				}
			}
			if mode == "lost runner" || mode == "duplicate result" {
				now = now.Add(run.ExternalLease + time.Second)
				if mode == "duplicate result" {
					if c, b := h.externalRequest(endpoint+"/result", token, run.Result{State: run.Succeeded}); c != 200 {
						t.Fatal(c, string(b))
					}
				}
			}
			h.tick()
			saved := podScheduleRun(h, item.ID)
			var next run.Job
			for _, j := range saved.Jobs {
				if j.Plan.Key == "second" {
					next = j
				}
			}
			if mode != "next poll" {
				if next.PodUID == "" || next.Plan.External {
					t.Fatalf("fallback did not resume: %+v", next)
				}
				return
			}
			if next.PodUID != "" || next.State != run.Queued {
				t.Fatalf("handoff raced to Pod: %+v", next)
			}
			if c, b := poll("d"); c != 200 {
				t.Fatal(c, string(b))
			}
			next = podScheduleRun(h, item.ID).Jobs[1]
			for _, j := range podScheduleRun(h, item.ID).Jobs {
				if j.Plan.Key == "second" {
					next = j
				}
			}
			if next.RunnerID != macProfile || next.PodUID != "" {
				t.Fatal("dependent job did not stay on device", next)
			}
		})
	}
}
