//go:build integration

package integration_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"golang.org/x/crypto/bcrypt"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
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
		t.Run("rootless BuildKit pushes immutable digest and cleans failed builders", func(t *testing.T) {
			hash, err := bcrypt.GenerateFromPassword([]byte("integration-registry-password"), bcrypt.MinCost)
			must(t, err)
			_, err = client.CoreV1().Secrets("forgelet").Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "registry-htpasswd"}, Data: map[string][]byte{"htpasswd": append([]byte("test:"), hash...)}}, metav1.CreateOptions{})
			must(t, err)
			_, err = client.CoreV1().Pods("forgelet").Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "test-registry", Labels: map[string]string{"app": "test-registry"}}, Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "auth", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "registry-htpasswd"}}}}, Containers: []corev1.Container{{Name: "registry", Image: "registry:2.8.3", Env: []corev1.EnvVar{{Name: "REGISTRY_AUTH", Value: "htpasswd"}, {Name: "REGISTRY_AUTH_HTPASSWD_REALM", Value: "integration"}, {Name: "REGISTRY_AUTH_HTPASSWD_PATH", Value: "/auth/htpasswd"}}, VolumeMounts: []corev1.VolumeMount{{Name: "auth", MountPath: "/auth", ReadOnly: true}}, Ports: []corev1.ContainerPort{{ContainerPort: 5000}}}}}}, metav1.CreateOptions{})
			must(t, err)
			_, err = client.CoreV1().Services("forgelet").Create(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "test-registry"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeNodePort, Selector: map[string]string{"app": "test-registry"}, Ports: []corev1.ServicePort{{Port: 5000, NodePort: 30500, TargetPort: intstr.FromInt32(5000)}}}}, metav1.CreateOptions{})
			must(t, err)
			waitFor(t, ctx, "test registry ready", func() bool {
				p, err := client.CoreV1().Pods("forgelet").Get(ctx, "test-registry", metav1.GetOptions{})
				if err != nil {
					return false
				}
				for _, c := range p.Status.Conditions {
					if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
						return true
					}
				}
				return false
			})
			image := registryImage(t, ctx, client)
			source := fmt.Sprintf(`on: push
jobs:
  image:
    runs-on: builder
    outputs: {digest: '${{ steps.build.outputs.digest }}'}
    steps:
      - run: |
          printf 'FROM busybox:1.37.0\nRUN mkdir -p /www && echo healthy > /www/index.html\nUSER 1000\nCMD ["httpd", "-f", "-p", "8080", "-h", "/www"]\n' > Dockerfile
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
