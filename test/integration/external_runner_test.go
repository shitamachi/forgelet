//go:build integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/shitamachi/forgelet/internal/executor"
	"github.com/shitamachi/forgelet/internal/run"
)

var fixtureDeviceToken = "frr_" + strings.Repeat("a", 64)

func configureExternalFixture(config *corev1.ConfigMap) {
	config.Data["forgelet.yaml"] = strings.Replace(config.Data["forgelet.yaml"], "  - test/project\n", "  - test/project\n  - test/runner-other\n", 1)
	config.Data["forgelet.yaml"] += fmt.Sprintf(`
runner_fallbacks:
  portable: homelab
external_runners:
  macos-arm64:
    repository: test/project
    workflow: .github/workflows/manual.yml
    ref: refs/heads/main
    token_sha256: %x
`, sha256.Sum256([]byte(fixtureDeviceToken)))
}

func testExternalRunner(t *testing.T, ctx context.Context, client *kubernetes.Clientset, httpClient *http.Client, endpoint string,
	create func(*testing.T, string) run.Run, get func(*testing.T, string) run.Run) {
	t.Run("external desktop runner shares capacity and survives control plane restart", func(t *testing.T) {
		const profile = "web-macos-arm64"
		var registration struct{ Token string }
		must(t, json.Unmarshal(api(t, ctx, httpClient, endpoint, "POST", "/api/runners", map[string]string{
			"name": profile,
		}, token, 201), &registration))
		mac := create(t, `on: push
jobs:
  native:
    runs-on: macos-arm64
    steps:
      - run: |
          uname -s > platform.txt
          echo external-before
          while [ ! -f resume ]; do sleep 0.1; done
          echo external-after
      - uses: actions/upload-artifact@v4
        with: {name: native-platform, path: platform.txt}
`)
		macJob := mac.Jobs[0]
		poll := map[string]string{"attempt": "frja_" + strings.Repeat("b", 64)}
		pod := create(t, `on: push
jobs:
  occupy:
    strategy:
      matrix:
        index: [1, 2]
    runs-on: homelab
    steps:
      - run: sleep 180
`)
		waitFor(t, ctx, "both slots occupied by Pod jobs", func() bool {
			jobs := get(t, pod.ID).Jobs
			return jobs[0].State == run.Running && jobs[1].State == run.Running
		})
		api(t, ctx, httpClient, endpoint, "POST", "/runner/v1/poll", poll, registration.Token, 204)
		if job := get(t, mac.ID).Jobs[0]; job.State != run.Queued || job.PodUID != "" {
			t.Fatalf("offline desktop job should remain queued: %+v", job)
		}
		api(t, ctx, httpClient, endpoint, "POST", "/api/runs/"+pod.ID+"/cancel", nil, token, 200)
		waitFor(t, ctx, "Pod slots released", func() bool { return get(t, pod.ID).State == run.Cancelled })
		api(t, ctx, httpClient, endpoint, "PUT", "/api/runners/"+profile+"/pause", map[string]bool{"paused": true}, token, 200)
		api(t, ctx, httpClient, endpoint, "POST", "/runner/v1/poll", poll, registration.Token, 204)
		api(t, ctx, httpClient, endpoint, "PUT", "/api/runners/"+profile+"/pause", map[string]bool{"paused": false}, token, 200)
		var grant run.ExternalGrant
		must(t, json.Unmarshal(api(t, ctx, httpClient, endpoint, "POST", "/runner/v1/poll", poll, registration.Token, 200), &grant))
		if grant.JobID != macJob.ID || grant.RunID != mac.ID {
			t.Fatalf("wrong grant: %+v", grant)
		}
		api(t, ctx, httpClient, endpoint, "POST", "/runner/v1/poll", poll, registration.Token, 200)
		api(t, ctx, httpClient, endpoint, "POST", "/internal/jobs/"+macJob.ID+"/claim", nil, poll["attempt"], 401)
		dir := t.TempDir()
		localCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		var executionErr error
		go func() {
			defer close(done)
			executionErr = executor.RunRemote(localCtx, executor.RemoteOptions{URL: endpoint, JobID: macJob.ID,
				AttemptToken: poll["attempt"], Workspace: dir, Client: httpClient, RetryTimeout: 45 * time.Second})
		}()
		t.Cleanup(func() {
			cancel()
			// A failing assertion must also stop the local process.
			select {
			case <-done:
			case <-time.After(time.Second):
			}
		})
		platformPath := filepath.Join(dir, "platform.txt")
		waitFor(t, ctx, "external executor wrote local workspace", func() bool {
			data, err := os.ReadFile(platformPath)
			want := "Linux"
			if runtime.GOOS == "darwin" {
				want = "Darwin"
			}
			return err == nil && strings.TrimSpace(string(data)) == want
		})
		restartServer(t, ctx, client, httpClient, endpoint)
		waitFor(t, ctx, "runner repository authorization revalidated", func() bool {
			var view struct {
				Repositories map[string]struct {
					State string `json:"state"`
				} `json:"repositories"`
			}
			must(t, json.Unmarshal(api(t, ctx, httpClient, endpoint, "GET", "/api/github/authorization", nil, token, 200), &view))
			return view.Repositories["test/project"].State == "authorized"
		})
		poll["job_id"] = macJob.ID
		api(t, ctx, httpClient, endpoint, "POST", "/runner/v1/renew", poll, registration.Token, 200)
		if job := get(t, mac.ID).Jobs[0]; job.State != run.Running || job.RunnerID != profile || job.AttemptID == "" || job.PodUID != "" {
			t.Fatalf("external attempt changed on server restart: %+v", job)
		}
		_, err := client.CoreV1().Pods("forgelet").Get(ctx, macJob.PodName, metav1.GetOptions{})
		if !apierrors.IsNotFound(err) {
			t.Fatalf("external attempt unexpectedly has a Pod: %v", err)
		}
		must(t, os.WriteFile(filepath.Join(dir, "resume"), nil, 0600))
		select {
		case <-done:
			must(t, executionErr)
		case <-time.After(20 * time.Second):
			t.Fatal("external runner did not report completion")
		}
		final := get(t, mac.ID)
		if final.State != run.Succeeded || final.Jobs[0].Result.LastLogSeq == 0 {
			t.Fatalf("external run did not succeed with logs: %+v", final)
		}
		artifacts := api(t, ctx, httpClient, endpoint, "GET", "/api/runs/"+mac.ID+"/artifacts", nil, token, 200)
		if !strings.Contains(string(artifacts), "native-platform") {
			t.Fatalf("missing desktop artifact: %s", artifacts)
		}
		second := createPublicWorkflow(t, ctx, httpClient, endpoint, map[string]any{
			"repository": "test/runner-other", "sha": strings.Repeat("a", 40), "ref": "refs/heads/main",
			"workflow": "on: push\njobs:\n  native:\n    runs-on: macos-arm64\n    steps:\n      - run: echo second-repository\n",
		})
		poll["attempt"] = "frja_" + strings.Repeat("c", 64)
		delete(poll, "job_id")
		must(t, json.Unmarshal(api(t, ctx, httpClient, endpoint, "POST", "/runner/v1/poll", poll, registration.Token, 200), &grant))
		if grant.Repository != second.Repository || grant.JobID != second.Jobs[0].ID {
			t.Fatalf("same credential did not receive the other repository: %+v", grant)
		}
		must(t, executor.RunRemote(ctx, executor.RemoteOptions{URL: endpoint, JobID: grant.JobID,
			AttemptToken: poll["attempt"], Workspace: t.TempDir(), Client: httpClient}))
		if job := get(t, second.ID).Jobs[0]; job.State != run.Succeeded || job.RunnerID != profile || job.PodUID != "" {
			t.Fatalf("other repository was not executed by the same runner: %+v", job)
		}
		var replacement struct{ Token string }
		must(t, json.Unmarshal(api(t, ctx, httpClient, endpoint, "POST", "/api/runners/"+profile+"/rotate", nil, token, 200), &replacement))
		poll["attempt"] = "frja_" + strings.Repeat("d", 64)
		api(t, ctx, httpClient, endpoint, "POST", "/runner/v1/poll", poll, registration.Token, 401)
		api(t, ctx, httpClient, endpoint, "POST", "/runner/v1/poll", poll, replacement.Token, 204)

		const portable = `on: push
jobs:
  native:
    runs-on: portable
    steps:
      - run: uname -sm
`
		preferred := create(t, portable)
		must(t, json.Unmarshal(api(t, ctx, httpClient, endpoint, "POST", "/runner/v1/poll", poll, replacement.Token, 200), &grant))
		if grant.JobID != preferred.Jobs[0].ID {
			t.Fatalf("wrong portable Runner grant: %+v", grant)
		}
		must(t, executor.RunRemote(ctx, executor.RemoteOptions{URL: endpoint, JobID: grant.JobID,
			AttemptToken: poll["attempt"], Workspace: t.TempDir(), Client: httpClient}))
		if job := get(t, preferred.ID).Jobs[0]; job.State != run.Succeeded || job.RunnerID != profile || job.PodUID != "" {
			t.Fatalf("portable job should run on the available Runner: %+v", job)
		}

		api(t, ctx, httpClient, endpoint, "PUT", "/api/runners/"+profile+"/pause", map[string]bool{"paused": true}, token, 200)
		fallback := create(t, portable)
		waitFor(t, ctx, "portable job executed in a real fallback Pod", func() bool {
			job := get(t, fallback.ID).Jobs[0]
			if job.State.Terminal() && job.State != run.Succeeded {
				t.Fatalf("Pod fallback failed: %+v", job.Result)
			}
			return job.State == run.Succeeded && job.PodUID != "" && job.Profile == "homelab" && job.RunnerID == ""
		})
	})
}
