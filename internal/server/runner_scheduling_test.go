package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/shitamachi/forgelet/internal/kube"
	"github.com/shitamachi/forgelet/internal/run"
)

func TestRunnerPreferenceAndPodFallback(t *testing.T) {
	for _, tc := range []struct {
		name, platforms                           string
		paused, busy, stale, podFirst, wantRunner bool
	}{
		{name: "prefer matching idle runner", platforms: "linux/amd64", wantRunner: true},
		{name: "offline"},
		{name: "incompatible", platforms: "linux/arm64"},
		{name: "no running builder", platforms: "none"},
		{name: "paused", platforms: "linux/amd64", paused: true},
		{name: "busy", platforms: "linux/amd64", busy: true},
		{name: "stale", platforms: "linux/amd64", stale: true},
		{name: "preference disabled", platforms: "linux/amd64", podFirst: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := externalHarness(t, 2)
			h.http.Close()
			const image = "registry.example.com/team/app"
			h.cfg.RunnerFallbacks = map[string]string{"portable": "small"}
			h.cfg.ExternalBuilds = []ExternalBuildPolicy{{
				Repository: "me/repo", Workflow: ".github/workflows/manual.yml", Ref: "refs/heads/main",
				Build: run.ExternalBuild{Image: image, Platform: "linux/amd64"},
			}}
			h.cfg.Profiles["small"] = kube.Profile{Image: "executor:test", Build: &kube.BuildProfile{Images: []string{image}}}
			if tc.podFirst {
				prefer := false
				h.cfg.PreferRunner = &prefer
			}
			h.start()
			poll := func(nonce string, want int) []byte {
				t.Helper()
				body, err := json.Marshal(runnerRequest{Attempt: "frja_" + strings.Repeat(nonce, 64)})
				if err != nil {
					t.Fatal(err)
				}
				code, data := h.request("POST", "/runner/v1/poll", string(body), map[string]string{
					"Content-Type": "application/json", "Authorization": "Bearer " + testMachineToken,
					run.RunnerBuildPlatformsHeader: tc.platforms,
				})
				if code != want {
					t.Fatalf("poll: %d, want %d: %s", code, want, data)
				}
				return data
			}
			if tc.platforms != "" {
				poll("a", 204)
			}
			if tc.busy {
				h.manual(strings.ReplaceAll(testWorkflow, "runs-on: small", "runs-on: macos-arm64"))
				poll("b", 200)
				poll("c", 204) // A busy device's poll cannot make it available.
			}
			if tc.paused {
				if code, body := h.admin("PUT", "/api/runners/"+macProfile+"/pause", map[string]bool{"paused": true}); code != 200 {
					t.Fatal(code, string(body))
				}
			}
			if tc.stale {
				now := h.server.now().Add(runnerReadyWindow + time.Second)
				h.server.now = func() time.Time { return now }
			}
			item := h.manual(`on: push
jobs:
  build:
    runs-on: portable
    steps:
      - id: build
        uses: forgelet/build@v1
        with: {image: registry.example.com/team/app}
`)
			if tc.podFirst {
				poll("d", 204)
			}
			h.tick()
			if tc.wantRunner {
				if h.runtime.created != 0 {
					t.Fatal("Pod started despite a ready Runner")
				}
				var grant run.ExternalGrant
				if err := json.Unmarshal(poll("d", 200), &grant); err != nil || grant.Platform != "linux/amd64" {
					t.Fatalf("wrong target: %+v %v", grant, err)
				}
			}
			job, err := h.store.GetJob(t.Context(), item.Jobs[0].ID)
			if err != nil || job.State != run.Starting || job.Plan.ExternalBuild == nil || job.Plan.ExternalBuild.Platform != "linux/amd64" {
				t.Fatalf("unexpected job: %+v %v", job, err)
			}
			if tc.wantRunner {
				if !job.Plan.External || job.RunnerID != macProfile || job.PodUID != "" {
					t.Fatalf("wrong Runner assignment: %+v", job)
				}
				return
			}
			if job.Plan.External || job.Profile != "small" || job.PodUID == "" || job.RunnerID != "" {
				t.Fatalf("wrong Pod fallback: %+v", job)
			}
			profile, err := h.server.fallbackPodProfile(job.Plan)
			if err != nil || profile.NodeSelector[corev1.LabelArchStable] != "amd64" || profile.NodeSelector[corev1.LabelOSStable] != "linux" {
				t.Fatalf("fallback placement: %+v %v", profile.NodeSelector, err)
			}
			poll("e", 204)
		})
	}
}
