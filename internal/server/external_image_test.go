package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/shitamachi/forgelet/internal/delivery"
	"github.com/shitamachi/forgelet/internal/kube"
	"github.com/shitamachi/forgelet/internal/run"
)

func TestExternalImageGatesAutomaticDeployment(t *testing.T) {
	for _, tc := range []struct {
		arch string
		want run.State
		pod  bool
	}{
		{"amd64", run.Succeeded, false},
		{"arm64", run.Failed, false},
		{"amd64", run.Succeeded, true},
		{"arm64", run.Failed, true},
	} {
		backend := "runner"
		if tc.pod {
			backend = "fallback"
		}
		t.Run(backend+"/"+tc.arch, func(t *testing.T) {
			reg := httptest.NewServer(registry.New())
			defer reg.Close()
			image := strings.TrimPrefix(reg.URL, "http://") + "/test/app"
			tag, err := name.NewTag(image+":test", name.Insecure)
			if err != nil {
				t.Fatal(err)
			}
			img, err := mutate.ConfigFile(empty.Image, &v1.ConfigFile{OS: "linux", Architecture: tc.arch})
			if err != nil {
				t.Fatal(err)
			}
			if err := remote.Write(tag, img, remote.WithContext(t.Context())); err != nil {
				t.Fatal(err)
			}
			digest, err := img.Digest()
			if err != nil {
				t.Fatal(err)
			}

			h := externalHarness(t, 1)
			h.http.Close()
			// The control plane decides the image platform; the device only has
			// to be able to build it.
			built := &run.ExternalBuild{Image: image, Platform: "linux/amd64", InsecureRegistry: true}
			h.cfg.ExternalBuilds = []ExternalBuildPolicy{{
				Repository: "me/repo", Workflow: ".github/workflows/manual.yml", Ref: "refs/heads/main", Build: *built,
			}}
			label := run.MacRunnerLabel
			var podCount int
			if tc.pod {
				label = "portable"
				h.cfg.RunnerFallbacks = map[string]string{label: "small"}
				profile := h.cfg.Profiles["small"]
				profile.Build = &kube.BuildProfile{Images: []string{image}, InsecureRegistry: true}
				h.cfg.Profiles["small"] = profile
				podCount = 1
			}
			h.cfg.Targets = map[string]delivery.Target{"web": {
				Name: "web", SourceRepository: "me/repo", SourceRef: "refs/heads/main",
				Repository: "me/gitops", InstallationID: 42, Branch: "main", Path: "apps/kustomization.yaml",
				Mode: "direct", Image: image, ImageName: "app", FluxNamespace: "flux-system",
				Kustomization: "app", Namespace: "apps", Deployment: "app", Container: "app",
				OnSuccess: &delivery.OnSuccess{WorkflowPath: ".github/workflows/manual.yml", Job: "publish", Profile: "small"},
			}}
			h.cfg.Delivery = &delivery.Coordinator{Store: h.store, Now: time.Now}
			h.start()

			source := `on: push
jobs:
  publish:
    runs-on: LABEL
    steps:
      - id: image
        uses: forgelet/build@v1
        with: {image: IMAGE}
`
			item := h.manual(strings.NewReplacer("IMAGE", image, "LABEL", label).Replace(source))
			if len(item.Jobs) != 2 {
				t.Fatalf("expected a publisher and a controller-owned deployment, got %+v", item.Jobs)
			}
			attempt := "frja_" + strings.Repeat("b", 64)
			var jobID string
			var call func(string, any) (int, []byte)
			if tc.pod {
				h.tick()
				for _, job := range item.Jobs {
					if job.Plan.ExternalBuild != nil {
						jobID = job.ID
						call = func(path string, input any) (int, []byte) {
							return h.runtimeRequest("POST", path, job, input)
						}
					}
				}
			} else {
				code, body := h.externalRequest("/poll", testMachineToken, runnerRequest{Attempt: attempt})
				var grant run.ExternalGrant
				if err := json.Unmarshal(body, &grant); code != 200 || err != nil || grant.Platform != "linux/amd64" {
					t.Fatalf("platform grant: %d %s", code, body)
				}
				jobID = grant.JobID
				call = func(path string, input any) (int, []byte) {
					return h.externalRequest("/jobs/"+jobID+"/"+path, attempt, input)
				}
			}
			code, body := call("claim", nil)
			var plan run.Plan
			if err := json.Unmarshal(body, &plan); code != 200 || err != nil || plan.External == tc.pod || plan.ExternalBuild == nil || *plan.ExternalBuild != *built {
				t.Fatalf("claimed plan: %d %s", code, body)
			}
			h.tick()
			if h.runtime.created != podCount {
				t.Fatal("deployment started before the image was verified")
			}
			result := run.Result{State: run.Succeeded, Outputs: map[string]string{run.ExternalImageDigestOutput: digest.String()}}
			checkResult := func() {
				t.Helper()
				code, body := call("result", result)
				var reply struct{ State run.State }
				if err := json.Unmarshal(body, &reply); code != 200 || err != nil || reply.State != tc.want {
					t.Fatalf("result: %d %s", code, body)
				}
			}
			checkResult()
			reg.Close()
			checkResult() // A response loss after commit does not need the registry again.
			h.tick()
			job, err := h.store.GetJob(t.Context(), jobID)
			if err != nil || job.State != tc.want {
				t.Fatalf("saved job: %+v, %v", job, err)
			}
			if tc.want == run.Succeeded && h.runtime.created != podCount+1 {
				t.Fatal("verified digest did not unblock deployment")
			}
			if tc.want == run.Failed && (h.runtime.created != podCount || !strings.Contains(job.Result.Error, "registry image is linux/arm64")) {
				t.Fatalf("wrong platform should block deployment: %+v", job.Result)
			}
		})
	}
}
