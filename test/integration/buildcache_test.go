//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/shitamachi/forgelet/internal/run"
)

func testBuildxCache(t *testing.T, ctx context.Context, cluster testcontainers.Container, client *kubernetes.Clientset, httpClient *http.Client, endpoint, image string, request func(*testing.T, string, string, any, int) []byte, create func(*testing.T, string) run.Run, get func(*testing.T, string) run.Run) {
	t.Run("independent BuildKit gha v2 cache cold warm restart failure and cancellation", func(t *testing.T) {
		// This functional layer exceeds 128 one-MiB client blocks. The larger
		// temporary filesystem accommodates a persisted object and upload copy;
		// the earlier disk-pressure test still uses its original 256 MiB mount.
		clusterCommand(t, ctx, cluster, "mount", "-o", "remount,size=512m", testStorageRoot)
		defer clusterCommand(t, ctx, cluster, "mount", "-o", "remount,size=256m", testStorageRoot)
		previousTimeout := httpClient.Timeout
		httpClient.Timeout = 2 * time.Minute
		defer func() { httpClient.Timeout = previousTimeout }()
		create = func(t *testing.T, source string) run.Run {
			return createPublicWorkflow(t, ctx, httpClient, endpoint, map[string]any{"repository": "test/project", "sha": strings.Repeat("a", 40), "ref": "refs/heads/main", "workflow": source})
		}
		ensureTestRegistry(t, ctx, client)
		runner := pushTestImage(t, ctx, cluster, client, image, "buildcache-runner:test")
		builder := pinTestRegistryImage(t, ctx, cluster, pushTestImage(t, ctx, cluster, client, "moby/buildkit:v0.20.2", "buildcache-builder:test"))
		base := pinTestRegistryImage(t, ctx, cluster, pushTestImage(t, ctx, cluster, client, "rancher/mirrored-library-busybox:1.37.0", "buildcache-base:test"))
		target := strings.Replace(registryImage(t, ctx, client), "forgelet-test", "buildcache-output", 1)
		registry := strings.SplitN(target, "/", 2)[0]
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
		profile := fmt.Sprintf("profiles:\n  buildcache:\n    image: %s\n    memory_limit: 2Gi\n    act:\n      runner_image: %s\n      registry_secret: forgelet-registry\n      insecure_registry: true\n      image_mirrors:\n        moby/buildkit:buildx-stable-1: %s\n      buildkit_config: |\n        [registry.\"%s\"]\n          http = true\n", image, runner, builder, registry)
		cm.Data["forgelet.yaml"] = strings.Replace(previous, "profiles:\n", profile, 1)
		_, err = client.CoreV1().ConfigMaps("forgelet").Update(ctx, cm, metav1.UpdateOptions{})
		must(t, err)
		restartServer(t, ctx, client, httpClient, endpoint)
		request(t, "PUT", "/api/repositories/test/project/secrets/CACHE_BUILD_SECRET", map[string]string{"value": "cache-build-test-secret"}, 200)
		defer request(t, "DELETE", "/api/repositories/test/project/secrets/CACHE_BUILD_SECRET", nil, 200)
		logs := func(item run.Run) string {
			var text strings.Builder
			for _, job := range item.Jobs {
				var after int64
				for {
					var page []run.Log
					must(t, json.Unmarshal(request(t, "GET", fmt.Sprintf("/api/jobs/%s/logs?after=%d", job.ID, after), nil, 200), &page))
					if len(page) == 0 {
						break
					}
					for _, line := range page {
						text.WriteString(line.Data)
						after = line.Seq
					}
				}
			}
			return text.String()
		}
		source := fmt.Sprintf(`on: push
permissions: {contents: read}
jobs:
  image:
    runs-on: buildcache
    steps:
      - uses: docker/setup-buildx-action@v3
      - run: |
          test "$ACTIONS_CACHE_SERVICE_V2" = true
          case "$ACTIONS_RESULTS_URL" in http://127.*) exit 9;; esac
          printf 'FROM %s\nRUN --mount=type=secret,id=TOKEN test "$(cat /run/secrets/TOKEN)" = cache-build-test-secret && head -c 135266304 /dev/urandom > /proof-data && echo cache-proof-executed > /proof\n' > Dockerfile
          touch prepared
          while [ ! -f continue ]; do sleep 1; done
      - uses: docker/login-action@v3
        with: {registry: '%s', username: test, password: integration-registry-password}
      - id: image
        uses: docker/build-push-action@v6
        with:
          context: .
          push: true
          tags: '%s:latest'
          cache-from: type=gha,scope=stage106c
          cache-to: type=gha,scope=stage106c,mode=max
          secrets: TOKEN=${{ secrets.CACHE_BUILD_SECRET }}
`, base, registry, target)
		cold := create(t, source)
		var active run.Job
		waitForTimeout(t, ctx, "buildx before restart", 6*time.Minute, func() bool {
			item := get(t, cold.ID)
			active = item.Jobs[0]
			if active.State.Terminal() {
				t.Fatal("buildx setup failed", item, logs(item))
			}
			if active.State != run.Running {
				return false
			}
			code, _, _ := cluster.Exec(ctx, []string{"kubectl", "exec", "-n", "forgelet", active.PodName, "-c", "executor", "--", "test", "-f", "/workspace/repository/prepared"})
			return code == 0
		})
		uid := active.PodUID
		restartServer(t, ctx, client, httpClient, endpoint)
		if got := get(t, cold.ID).Jobs[0]; got.PodUID != uid || got.State != run.Running {
			t.Fatal("lost active builder", got)
		}
		clusterCommand(t, ctx, cluster, "kubectl", "exec", "-n", "forgelet", active.PodName, "-c", "executor", "--", "touch", "/workspace/repository/continue")
		finish := func(item run.Run) run.Run {
			waitForTimeout(t, ctx, "buildcache terminal", 8*time.Minute, func() bool { return get(t, item.ID).State.Terminal() })
			item = get(t, item.ID)
			if item.State != run.Succeeded {
				t.Fatal("buildcache failure", item, logs(item))
			}
			return item
		}
		cold = finish(cold)
		coldDigest := jobByKey(t, cold, "image").Result.Steps[3].Outputs["digest"]
		if coldDigest == "" || !strings.HasSuffix(pinTestRegistryImage(t, ctx, cluster, target+":latest"), "@"+coldDigest) {
			t.Fatal("cold publication digest mismatch", cold)
		}
		warm := finish(create(t, strings.Replace(source, "while [ ! -f continue ]; do sleep 1; done", "true", 1)))
		if warm.Jobs[0].PodUID == uid {
			t.Fatal("cache test reused original engine")
		}
		text := logs(warm)
		if !strings.Contains(text, "importing cache manifest from gha") || !regexp.MustCompile(`RUN --mount=type=secret[^\n]*\n\s*#[0-9]+ CACHED`).MatchString(text) {
			t.Fatal("warm build did not restore the actual RUN layer", text)
		}
		if strings.Contains(text, "cache-build-test-secret") {
			t.Fatal("build secret was not masked")
		}
		failSource := strings.Replace(strings.Replace(source, "while [ ! -f continue ]; do sleep 1; done", "true", 1), "test \"$(cat /run/secrets/TOKEN)\" = cache-build-test-secret", "test false = true", 1)
		// Change the RUN instruction, so the failure cannot be hidden by a warm hit.
		failed := create(t, failSource)
		waitForTimeout(t, ctx, "failed build", 6*time.Minute, func() bool { return get(t, failed.ID).State.Terminal() })
		if got := get(t, failed.ID); got.State != run.Failed {
			t.Fatal("failed RUN published successfully", got, logs(got))
		}
		if !strings.HasSuffix(pinTestRegistryImage(t, ctx, cluster, target+":latest"), "@"+jobByKey(t, warm, "image").Result.Steps[3].Outputs["digest"]) {
			t.Fatal("failed build changed published image")
		}
		cancelSource := strings.Replace(strings.Replace(source, "while [ ! -f continue ]; do sleep 1; done", "true", 1), "test \"$(cat /run/secrets/TOKEN)\" = cache-build-test-secret", "echo buildcache-cancel-ready; sleep 300; test false = true", 1)
		cancelled := create(t, cancelSource)
		waitForTimeout(t, ctx, "independent builder active before cancellation", 6*time.Minute, func() bool {
			job := get(t, cancelled.ID).Jobs[0]
			if job.State.Terminal() {
				t.Fatal("build completed before cancellation", job, logs(cancelled))
			}
			if job.State != run.Running {
				return false
			}
			// Buildx may buffer progress until its RUN finishes. Observe the
			// actual RUN process in this job's independent builder instead.
			code, output, err := cluster.Exec(ctx, []string{"kubectl", "exec", "-n", "forgelet", job.PodName, "-c", "executor", "--", "sh", "-c", `builder=$(docker --host unix:///run/forgelet-act/docker.sock ps --filter name=buildx_buildkit_ --format '{{.ID}}'); test -n "$builder" && docker --host unix:///run/forgelet-act/docker.sock exec "$builder" ps`})
			if err != nil || code != 0 {
				return false
			}
			data, err := io.ReadAll(output)
			return err == nil && strings.Contains(string(data), "/bin/sh -c echo buildcache-cancel-ready; sleep 300;")
		})
		request(t, "POST", "/api/runs/"+cancelled.ID+"/cancel", nil, 200)
		waitFor(t, ctx, "independent BuildKit cancelled", func() bool { return get(t, cancelled.ID).State == run.Cancelled })
		waitFor(t, ctx, "BuildKit job engines removed", func() bool {
			pods, err := client.CoreV1().Pods("forgelet").List(ctx, metav1.ListOptions{LabelSelector: "forgelet.dev/job"})
			return err == nil && len(pods.Items) == 0
		})
		t.Logf("independent BuildKit v0.20.2: cold=%s warm=%s originalUID=%s digest=%s; failed=%s cancelled=%s", cold.ID, warm.ID, uid, coldDigest, failed.ID, cancelled.ID)
	})
}
