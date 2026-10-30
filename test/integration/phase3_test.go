//go:build integration

package integration_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	configyaml "gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/shitamachi/forgelet/internal/run"
)

func registryImage(t *testing.T, ctx context.Context, client kubernetes.Interface) string {
	t.Helper()
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	must(t, err)
	for _, node := range nodes.Items {
		for _, address := range node.Status.Addresses {
			if address.Type == corev1.NodeInternalIP {
				return address.Address + ":30500/forgelet-test"
			}
		}
	}
	t.Fatal("test node has no IP")
	return ""
}
func testPhaseThree(t *testing.T, ctx context.Context, cluster testcontainers.Container, client *kubernetes.Clientset, httpClient *http.Client, endpoint, root string, request func(*testing.T, string, string, any, int) []byte, create func(*testing.T, string) run.Run, get func(*testing.T, string) run.Run) {
	logs := func(t *testing.T, jobID string) string {
		t.Helper()
		var entries []run.Log
		must(t, json.Unmarshal(request(t, "GET", "/api/jobs/"+jobID+"/logs", nil, 200), &entries))
		var joined strings.Builder
		for _, entry := range entries {
			joined.WriteString(entry.Data)
		}
		return joined.String()
	}
	t.Run("CI to CD", func(t *testing.T) {
		t.Run("small caches reserve actual bytes and evict cold cache across restart", func(t *testing.T) {
			cm, err := client.CoreV1().ConfigMaps("forgelet").Get(ctx, "forgelet-config", metav1.GetOptions{})
			must(t, err)
			original := cm.Data["forgelet.yaml"]
			t.Cleanup(func() {
				current, err := client.CoreV1().ConfigMaps("forgelet").Get(ctx, "forgelet-config", metav1.GetOptions{})
				must(t, err)
				current.Data["forgelet.yaml"] = original
				_, err = client.CoreV1().ConfigMaps("forgelet").Update(ctx, current, metav1.UpdateOptions{})
				must(t, err)
				restartServer(t, ctx, client, httpClient, endpoint)
			})
			output := clusterCommand(t, ctx, cluster, "kubectl", "-n", "forgelet", "exec", "deployment/forgelet", "-c", "github-test-api", "--", "/opt/forgelet/node24/bin/node", "-e", `const fs=require('fs');let sum=0;for(const d of fs.readdirSync('/fixture-state/blobs')){const f='/fixture-state/blobs/'+d+'/metadata.json';if(fs.existsSync(f))sum+=JSON.parse(fs.readFileSync(f)).size;}console.log('SIZE='+sum)`)
			at := strings.LastIndex(output, "SIZE=")
			if at < 0 {
				t.Fatal("missing byte inventory")
			}
			used, err := strconv.ParseInt(strings.TrimSpace(output[at+5:]), 10, 64)
			must(t, err)
			var cfg map[string]any
			must(t, configyaml.Unmarshal([]byte(original), &cfg))
			cfg["blobs"] = map[string]any{"object_bytes": 512 << 10, "cache_object_bytes": 512 << 10, "total_bytes": used + (512 << 10), "objects": 4096}
			encoded, err := configyaml.Marshal(cfg)
			must(t, err)
			cm.Data["forgelet.yaml"] = string(encoded)
			_, err = client.CoreV1().ConfigMaps("forgelet").Update(ctx, cm, metav1.UpdateOptions{})
			must(t, err)
			restartServer(t, ctx, client, httpClient, endpoint)
			item := create(t, `on: push
jobs:
  save:
    runs-on: homelab
    steps:
      - run: mkdir -p large small; dd if=/dev/zero of=large/data bs=1024 count=384; dd if=/dev/zero of=small/data bs=1024 count=16
      - uses: actions/cache/save@v4
        with: {key: actual-quota-large, path: large}
      - uses: actions/cache/save@v4
        with: {key: actual-quota-small, path: small}
`)
			if result := finishRun(t, ctx, get, item.ID); result.State != run.Succeeded {
				t.Fatal(result, logs(t, result.Jobs[0].ID))
			}
			over := create(t, `on: push
jobs:
  exceed:
    runs-on: homelab
    steps:
      - run: mkdir -p excess; dd if=/dev/zero of=excess/data bs=1024 count=200
      - uses: actions/cache/save@v4
        with: {key: actual-quota-overflow, path: excess}
`)
			if result := finishRun(t, ctx, get, over.ID); result.State != run.Succeeded {
				t.Fatal("cache pressure was not reclaimed", result)
			}
			restartServer(t, ctx, client, httpClient, endpoint)
			restored := create(t, `on: push
jobs:
  verify:
    runs-on: homelab
    steps:
      - uses: actions/cache/restore@v4
        with: {key: actual-quota-overflow, path: excess, fail-on-cache-miss: true}
      - uses: actions/cache/restore@v4
        with: {key: actual-quota-small, path: small, fail-on-cache-miss: true}
      - run: test "$(wc -c < excess/data)" -eq 204800; test "$(wc -c < small/data)" -eq 16384
`)
			if result := finishRun(t, ctx, get, restored.ID); result.State != run.Succeeded {
				t.Fatal(result, logs(t, result.Jobs[0].ID))
			}

			oversized := create(t, `on: push
jobs:
  oversized:
    runs-on: homelab
    steps:
      - run: mkdir oversized; dd if=/dev/zero of=oversized/data bs=1024 count=600
      - uses: actions/cache/save@v4
        with: {key: actual-quota-oversized, path: oversized}
`)
			if result := finishRun(t, ctx, get, oversized.ID); result.State != run.Failed {
				t.Fatal("per-object limit bypassed by eviction", result)
			}
		})
		t.Run("artifact and cache persist across pods and server restart", func(t *testing.T) {
			source := `on: push
jobs:
  produce:
    runs-on: homelab
    steps:
      - run: mkdir -p dist cache; echo artifact-content > dist/result; echo cache-content > cache/dependency
      - uses: actions/upload-artifact@v4
        with: {name: bundle, path: dist}
      - uses: actions/cache/save@v4
        with: {key: integration-dependency-v1, path: cache}
  consume:
    needs: produce
    runs-on: homelab
    steps:
      - uses: actions/download-artifact@v4
        with: {name: bundle, path: restored}
      - id: dependency
        uses: actions/cache/restore@v4
        with: {key: integration-dependency-v1, path: cache, fail-on-cache-miss: true}
      - run: test "$(cat restored/result)" = artifact-content; test "$(cat cache/dependency)" = cache-content; test '${{ steps.dependency.outputs.cache-hit }}' = true
`
			item := create(t, source)
			finished := finishRun(t, ctx, get, item.ID)
			if finished.State != run.Succeeded {
				t.Fatalf("artifact/cache run: %+v", finished)
			}
			var artifacts []map[string]any
			must(t, json.Unmarshal(request(t, "GET", "/api/runs/"+item.ID+"/artifacts", nil, 200), &artifacts))
			if len(artifacts) != 1 {
				t.Fatal(artifacts)
			}
			restartServer(t, ctx, client, httpClient, endpoint)
			other := create(t, `on: push
jobs:
  restored:
    runs-on: homelab
    steps:
      - uses: actions/cache/restore@v4
        with: {key: integration-dependency-v1, path: cache, fail-on-cache-miss: true}
      - run: test "$(cat cache/dependency)" = cache-content
`)
			if result := finishRun(t, ctx, get, other.ID); result.State != run.Succeeded {
				t.Fatal(result)
			}
		})
		t.Run("branch cache fallback and write isolation across restart", func(t *testing.T) {
			runEvent := func(ref, event, source string) run.Run {
				body := map[string]any{
					"repository": "test/project", "sha": strings.Repeat("a", 40), "ref": ref, "workflow": source,
				}
				if event != "" {
					body["event_name"] = event
				}
				var item run.Run
				must(t, json.Unmarshal(request(t, "POST", "/api/runs", body, 201), &item))
				done := finishRun(t, ctx, get, item.ID)
				if done.State != run.Succeeded {
					t.Fatalf("%s: %+v logs=%s", ref, done, logs(t, done.Jobs[0].ID))
				}
				return done
			}
			runAt := func(ref, source string) run.Run { return runEvent(ref, "", source) }
			runAt("refs/heads/main", `on: push
jobs:
  seed:
    runs-on: homelab
    steps:
      - run: mkdir -p branch-cache; echo main-dependency > branch-cache/data
      - uses: actions/cache/save@v4
        with: {key: branch-fallback-main-v1, path: branch-cache}
`)
			restartServer(t, ctx, client, httpClient, endpoint)
			runAt("refs/heads/feature/cache", `on: push
jobs:
  restore:
    runs-on: homelab
    steps:
      - id: restored
        uses: actions/cache/restore@v4
        with: {key: branch-fallback-main-v1, path: branch-cache, fail-on-cache-miss: true}
      - run: test "$(cat branch-cache/data)" = main-dependency; test '${{ steps.restored.outputs.cache-hit }}' = true; echo topic-dependency > branch-cache/data
      - uses: actions/cache/save@v4
        with: {key: branch-fallback-topic-v1, path: branch-cache}
`)
			restartServer(t, ctx, client, httpClient, endpoint)
			runAt("refs/heads/main", `on: push
jobs:
  isolated:
    runs-on: homelab
    steps:
      - id: private
        uses: actions/cache/restore@v4
        with: {key: branch-fallback-topic-v1, path: branch-cache}
      - run: test '${{ steps.private.outputs.cache-hit }}' = ''; test ! -e branch-cache/data
      - uses: actions/cache/restore@v4
        with: {key: branch-fallback-main-v1, path: branch-cache, fail-on-cache-miss: true}
      - run: test "$(cat branch-cache/data)" = main-dependency
`)
			// A manual run on a ref that never wrote its own cache must also
			// reuse the trusted default-branch object. This is the branch-cold
			// path that previously kept a full workload outside its 20-minute
			// budget: the fetch is bounded and original tests/assertions stay.
			runEvent("refs/heads/stage13-cold-20261030", "workflow_dispatch", `on: [push, workflow_dispatch]
jobs:
  cold:
    runs-on: homelab
    timeout-minutes: 1
    steps:
      - id: restored
        uses: actions/cache/restore@v4
        with: {key: branch-fallback-main-v1, path: branch-cache, fail-on-cache-miss: true}
      - run: test "$(cat branch-cache/data)" = main-dependency; test '${{ steps.restored.outputs.cache-hit }}' = true
`)
		})
		t.Run("rootless BuildKit pushes immutable digest and cleans failed builders", func(t *testing.T) {
			ensureTestRegistry(t, ctx, client)
			image := registryImage(t, ctx, client)
			source := fmt.Sprintf(`on: push
jobs:
  image:
    runs-on: builder
    outputs: {digest: '${{ steps.build.outputs.digest }}'}
    steps:
      - run: |
          cp -R /opt/forgelet/test-rootfs rootfs
          printf 'FROM scratch\nCOPY rootfs/ /\nENV PATH=/bin:/usr/bin:/sbin:/usr/sbin\nRUN mkdir -p /www && echo healthy > /www/index.html\nUSER 1000\nCMD ["httpd", "-f", "-p", "8080", "-h", "/www"]\n' > Dockerfile
      - id: build
        uses: forgelet/build@v1
        with: {image: %s}
      - run: cat /var/run/forgelet-registry/config.json
`, image)
			item := create(t, source)
			result := finishRun(t, ctx, get, item.ID)
			if result.State != run.Succeeded {
				for _, job := range result.Jobs {
					t.Log(string(request(t, "GET", "/api/jobs/"+job.ID+"/logs", nil, 200)))
				}
				t.Fatalf("build failed: %+v", result)
			}
			logText := logs(t, result.Jobs[0].ID)
			if strings.Contains(logText, "integration-registry-password") || strings.Contains(logText, base64.StdEncoding.EncodeToString([]byte("test:integration-registry-password"))) {
				t.Fatal("registry credential leaked into logs")
			}
			digest := result.Jobs[0].Result.Outputs["digest"]
			if !run.ValidDigest(digest) {
				t.Fatal("missing immutable digest", digest)
			}
			testFluxDelivery(t, ctx, cluster, client, httpClient, endpoint, root, image, digest, source, request, create, get)

			failed := create(t, strings.Replace(source, "RUN mkdir -p /www && echo healthy > /www/index.html", "RUN exit 7", 1))
			if got := finishRun(t, ctx, get, failed.ID); got.State != run.Failed {
				t.Fatalf("bad Dockerfile not failed: %+v", got)
			}
			// Emit enough data after the marker to release the masker's bounded
			// tail while RUN is still sleeping; do not wait for build completion.
			cancelled := create(t, strings.Replace(source, "RUN mkdir -p /www && echo healthy > /www/index.html", "RUN echo forgelet-build-running; seq 1 100; sleep 90", 1))
			waitFor(t, ctx, "BuildKit RUN is executing before cancellation", func() bool {
				output := logs(t, cancelled.Jobs[0].ID)
				if current := get(t, cancelled.ID); current.State.Terminal() {
					t.Fatalf("build finished before cancellation: %s\n%s", current.State, output)
				}
				for _, line := range strings.Split(output, "\n") {
					if strings.Contains(line, "forgelet-build-running") && !strings.Contains(line, "RUN ") {
						return true
					}
				}
				return false
			})
			request(t, "POST", "/api/runs/"+cancelled.ID+"/cancel", nil, 200)
			if stopped := finishRun(t, ctx, get, cancelled.ID); stopped.State != run.Cancelled {
				t.Fatal(stopped)
			}
			waitFor(t, ctx, "all builder pods removed", func() bool {
				pods, err := client.CoreV1().Pods("forgelet").List(ctx, metav1.ListOptions{LabelSelector: "forgelet.dev/job"})
				return err == nil && len(pods.Items) == 0
			})
		})
	})
}

func finishRun(t *testing.T, ctx context.Context, get func(*testing.T, string) run.Run, id string) run.Run {
	t.Helper()
	var result run.Run
	waitFor(t, ctx, "phase 3 workflow finishes", func() bool { result = get(t, id); return result.State.Terminal() })
	return result
}
