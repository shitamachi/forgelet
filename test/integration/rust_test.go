//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/shitamachi/forgelet/internal/run"
)

// Supply the locally loaded shipped Rust profile, without embedding credentials
// or source fixtures in that production image. The cluster remains disposable.
func testRustProfile(t *testing.T, ctx context.Context, cluster testcontainers.Container, client *kubernetes.Clientset, httpClient *http.Client, endpoint, supervisorImage string, request func(*testing.T, string, string, any, int) []byte, get func(*testing.T, string) run.Run) {
	t.Run("Rust PowerShell profile outputs errors restart and cancellation", func(t *testing.T) {
		image := os.Getenv("FORGELET_TEST_RUST_IMAGE")
		if image == "" {
			t.Skip("set FORGELET_TEST_RUST_IMAGE to the locally loaded production Rust profile")
		}
		ensureTestRegistry(t, ctx, client)
		runner := pinTestRegistryImage(t, ctx, cluster, pushTestImage(t, ctx, cluster, client, image, "rust-runner:test"))
		cm, err := client.CoreV1().ConfigMaps("forgelet").Get(ctx, "forgelet-config", metav1.GetOptions{})
		must(t, err)
		previous := cm.Data["forgelet.yaml"]
		defer func() {
			cm, err := client.CoreV1().ConfigMaps("forgelet").Get(ctx, "forgelet-config", metav1.GetOptions{})
			must(t, err)
			cm.Data["forgelet.yaml"] = previous
			_, err = client.CoreV1().ConfigMaps("forgelet").Update(ctx, cm, metav1.UpdateOptions{})
			must(t, err)
			restartServer(t, ctx, client, httpClient, endpoint)
		}()
		cm.Data["forgelet.yaml"] = strings.Replace(previous, "profiles:\n", fmt.Sprintf("profiles:\n  rust:\n    image: %s\n    memory_limit: 1Gi\n    workspace_limit: 8Gi\n    act:\n      runner_image: %s\n      registry_secret: forgelet-registry\n      insecure_registry: true\n", supervisorImage, runner), 1)
		_, err = client.CoreV1().ConfigMaps("forgelet").Update(ctx, cm, metav1.UpdateOptions{})
		must(t, err)
		restartServer(t, ctx, client, httpClient, endpoint)
		launch := func(source string) run.Run {
			return createPublicWorkflow(t, ctx, httpClient, endpoint, map[string]any{"repository": "test/project", "sha": strings.Repeat("a", 40), "ref": "refs/heads/main", "workflow": source})
		}
		logs := func(item run.Run) string {
			var text strings.Builder
			var after int64
			for {
				var page []run.Log
				must(t, json.Unmarshal(request(t, "GET", fmt.Sprintf("/api/jobs/%s/logs?after=%d", item.Jobs[0].ID, after), nil, 200), &page))
				if len(page) == 0 {
					return text.String()
				}
				for _, line := range page {
					text.WriteString(line.Data)
					after = line.Seq
				}
			}
		}
		workflow := `on: push
defaults: {run: {shell: pwsh}}
jobs:
  rust:
    runs-on: rust
    steps:
      - id: powershell
        run: |
          if ($PSVersionTable.PSVersion.ToString() -ne '7.6.9') { throw 'wrong PowerShell' }
          'proof=actual-pwsh' >> $env:GITHUB_OUTPUT
          'PROFILE_STATE=shared' >> $env:GITHUB_ENV
      - shell: bash
        run: |
          rustc --version | grep 'rustc 1.97.0'
          cargo new --lib --vcs none tiny
          cd tiny
          cargo fmt --all -- --check
          cargo clippy --offline --all-targets -- -D warnings
          cargo test --offline
      - run: |
          if ($env:PROFILE_STATE -ne 'shared' -or '${{ steps.powershell.outputs.proof }}' -ne 'actual-pwsh') { throw 'lost state' }
          Write-Output 'rust-pwsh-checkpoint'
          Write-Output ('x' * 2048)
          while (-not (Test-Path '/workspace/repository/continue')) { Start-Sleep -Seconds 1 }
`
		item := launch(workflow)
		waitForTimeout(t, ctx, "PowerShell checkpoint", 8*time.Minute, func() bool {
			if current := get(t, item.ID); current.State.Terminal() {
				t.Fatalf("Rust profile failed: %+v logs=%s", current, logs(item))
			}
			return strings.Contains(logs(item), "rust-pwsh-checkpoint")
		})
		before := get(t, item.ID).Jobs[0]
		pod, err := client.CoreV1().Pods("forgelet").Get(ctx, before.PodName, metav1.GetOptions{})
		must(t, err)
		if len(pod.Spec.Volumes) == 0 || pod.Spec.Volumes[0].Name != "workspace" || pod.Spec.Volumes[0].EmptyDir == nil || pod.Spec.Volumes[0].EmptyDir.SizeLimit == nil || pod.Spec.Volumes[0].EmptyDir.SizeLimit.String() != "8Gi" {
			t.Fatal("configured workspace quota missing from actual Pod", pod.Spec.Volumes)
		}
		restartServer(t, ctx, client, httpClient, endpoint)
		if after := get(t, item.ID).Jobs[0]; after.PodUID != before.PodUID || after.State != run.Running {
			t.Fatal("attempt replaced", after)
		}
		execInPod(t, ctx, cluster, before.PodName, "touch /workspace/repository/continue")
		if done := finishRun(t, ctx, get, item.ID); done.State != run.Succeeded {
			t.Fatal(done, logs(item))
		}
		bad := launch("on: push\njobs:\n  bad:\n    runs-on: rust\n    steps:\n      - shell: pwsh\n        run: Write-Error 'intentional-pwsh-failure'\n")
		if failed := finishRun(t, ctx, get, bad.ID); failed.State != run.Failed || failed.Jobs[0].Result.Steps[0].ExitCode == 0 {
			t.Fatal("PowerShell error swallowed", failed)
		}
		cancelled := launch(strings.Replace(workflow, "rust-pwsh-checkpoint", "rust-pwsh-cancel", 1))
		waitForTimeout(t, ctx, "PowerShell running before cancel", 8*time.Minute, func() bool { return strings.Contains(logs(cancelled), "rust-pwsh-cancel") })
		active := get(t, cancelled.ID).Jobs[0]
		request(t, "POST", "/api/runs/"+cancelled.ID+"/cancel", nil, 200)
		waitFor(t, ctx, "PowerShell attempt cancelled", func() bool { return get(t, cancelled.ID).State == run.Cancelled })
		waitFor(t, ctx, "PowerShell Pod and its owned engine removed", func() bool {
			_, err := client.CoreV1().Pods("forgelet").Get(ctx, active.PodName, metav1.GetOptions{})
			return apierrors.IsNotFound(err)
		})
	})
}
