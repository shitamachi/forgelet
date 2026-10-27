//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"golang.org/x/crypto/bcrypt"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"

	"github.com/shitamachi/forgelet/internal/run"
)

func testActJobs(t *testing.T, ctx context.Context, cluster testcontainers.Container, client *kubernetes.Clientset, httpClient *http.Client, endpoint, image string, request func(*testing.T, string, string, any, int) []byte, create func(*testing.T, string) run.Run, get func(*testing.T, string) run.Run) {
	t.Run("act job protocol runtimes cache restart cancellation and failure", func(t *testing.T) {
		previousTimeout := httpClient.Timeout
		httpClient.Timeout = 2 * time.Minute
		defer func() { httpClient.Timeout = previousTimeout }()
		create = func(t *testing.T, source string) run.Run {
			return createPublicWorkflow(t, ctx, httpClient, endpoint, map[string]any{"repository": "test/project", "sha": strings.Repeat("a", 40), "ref": "refs/heads/main", "workflow": source})
		}
		ensureTestRegistry(t, ctx, client)
		runner := pushTestImage(t, ctx, cluster, client, image, "act-runner:test")
		mirror := pinTestRegistryImage(t, ctx, cluster, pushTestImage(t, ctx, cluster, client, "rancher/mirrored-library-busybox:1.37.0", "mirrored-busybox:test"))
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
		cm.Data["forgelet.yaml"] = strings.Replace(previous, "profiles:\n", fmt.Sprintf("profiles:\n  actions:\n    image: %s\n    act:\n      runner_image: %s\n      insecure_registry: true\n      registry_secret: forgelet-registry\n      image_mirrors:\n        forgelet-mirror-probe:stage10d: %s\n    memory_limit: 1Gi\n", image, runner, mirror), 1)
		_, err = client.CoreV1().ConfigMaps("forgelet").Update(ctx, cm, metav1.UpdateOptions{})
		must(t, err)
		restartServer(t, ctx, client, httpClient, endpoint)
		secret := "act-split-private-secret"
		request(t, "PUT", "/api/repositories/test/project/secrets/TOKEN", map[string]string{"value": secret}, 200)
		defer request(t, "DELETE", "/api/repositories/test/project/secrets/TOKEN", nil, 200)
		actJob := func(item run.Run) run.Job {
			for _, job := range item.Jobs {
				if job.Plan.Kernel == "act" {
					return job
				}
			}
			t.Fatal("missing act leaf")
			return run.Job{}
		}
		logs := func(item run.Run) string {
			var out strings.Builder
			var after int64
			for {
				var page []run.Log
				must(t, json.Unmarshal(request(t, "GET", fmt.Sprintf("/api/jobs/%s/logs?after=%d", actJob(item).ID, after), nil, 200), &page))
				if len(page) == 0 {
					return out.String()
				}
				for _, chunk := range page {
					out.WriteString(chunk.Data)
					after = chunk.Seq
				}
			}
		}
		waitDone := func(item run.Run) run.Run {
			var result run.Run
			waitForTimeout(t, ctx, "act job terminal", 8*time.Minute, func() bool { result = get(t, item.ID); return result.State.Terminal() })
			if result.State != run.Succeeded {
				t.Fatalf("act failure: %+v logs=%s", result, logs(item))
			}
			return result
		}
		source := `on: push
jobs:
  act:
    runs-on: actions
    outputs: {answer: '${{ steps.node.outputs.answer }}'}
    env: {TOKEN: '${{ secrets.TOKEN }}', CACHE_UPLOAD_CHUNK_SIZE: '64'}
    steps:
      - run: |
          test ! -e /var/run/forgelet/token
          test ! -e /var/run/forgelet-act-registry/config.json
          docker run --rm --network none forgelet-mirror-probe:stage10d sh -c 'echo mirrored-public-image-ready'
          git init .
          git config user.email fixture@example.test
          git config user.name fixture
          git status --porcelain
          test -z "${TOKEN_FILE:-}"
          cp -R /opt/forgelet/test-actions/node20 ./node20
          cp -R /opt/forgelet/test-actions/node24 ./node24
          cp -R /opt/forgelet/test-actions/docker ./docker
          mkdir -p cache
      - uses: ./node20
      - id: node
        uses: ./node24
        with: {value: real-act}
      - id: docker
        uses: ./docker
        with: {value: docker-real-act}
      - run: test '${{ steps.docker.outputs.answer }}' = docker-real-act
      - id: cache
        uses: actions/cache@v4
        with: {path: cache, key: formal-act-cache-v2}
      - run: |
          printf '%s' "$TOKEN"
          printf '\n'
          if [ '${{ steps.cache.outputs.cache-hit }}' = true ]; then
            test "$(cat cache/item)" = stored
            sha256sum -c cache/checksum
          else
            echo stored > cache/item
            dd if=/dev/urandom of=cache/sdk-block bs=1048576 count=65
            sha256sum cache/sdk-block > cache/checksum
          fi
          printf 'formal-%s\n' act-ready
          printf '%2048s\n' ''
          while [ ! -f /workspace/repository/continue ]; do sleep 1; done
`
		createCall := func(child string) run.Run {
			caller := `on:
  workflow_dispatch:
    inputs: {enabled: {type: boolean, default: true}}
jobs:
  call:
    uses: ./.github/workflows/act-child.yml
    with: {enabled: '${{ inputs.enabled }}'}
    secrets: inherit
  after:
    needs: call
    runs-on: homelab
    steps:
      - run: test '${{ needs.call.outputs.answer }}' = real-act
`
			child = strings.Replace(child, "on: push", `on:
  workflow_call:
    inputs: {enabled: {type: boolean, default: false}}
    secrets: {TOKEN: {required: true}}
    outputs: {answer: {value: '${{ jobs.act.outputs.answer }}'}}`, 1)
			child = strings.Replace(child, "test ! -e /var/run/forgelet/token", "test '${{ inputs.enabled }}' = true\n          test '${{ github.job }}' = act\n          test ! -e /var/run/forgelet/token", 1)
			var result run.Run
			must(t, json.Unmarshal(request(t, "POST", "/api/runs", map[string]any{"repository": "test/project", "sha": strings.Repeat("a", 40), "ref": "refs/heads/main", "event_name": "workflow_dispatch", "workflow": caller, "workflow_files": map[string]string{".github/workflows/act-child.yml": child}}, 201), &result))
			return result
		}
		source = strings.Replace(source, "cp -R /opt/forgelet/test-actions/docker ./docker", "cp -R /opt/forgelet/test-actions/docker ./docker\n          sed -i '1cFROM forgelet-mirror-probe:stage10d' docker/Dockerfile", 1)
		item := createCall(source)
		waitForTimeout(t, ctx, "act reached restart checkpoint", 8*time.Minute, func() bool {
			current := get(t, item.ID)
			if current.State.Terminal() {
				t.Fatalf("act stopped before checkpoint: %+v logs=%s", current, logs(item))
			}
			return strings.Contains(logs(item), "formal-act-ready")
		})
		before := actJob(get(t, item.ID))
		if before.PodUID == "" || before.Plan.Kernel != "act" || len(before.Plan.ActionPins["actions/cache@v4"]) != 40 {
			t.Fatal("missing immutable Action snapshot", before)
		}
		restartServer(t, ctx, client, httpClient, endpoint)
		after := actJob(get(t, item.ID))
		if after.PodUID != before.PodUID || after.State != run.Running || after.Plan.ActionPins["actions/cache@v4"] != before.Plan.ActionPins["actions/cache@v4"] {
			t.Fatal("act attempt was replaced on restart", after)
		}
		// Touch only the supervisor's Pod-owned workspace, using its original UID.
		pod, err := client.CoreV1().Pods("forgelet").Get(ctx, before.PodName, metav1.GetOptions{})
		must(t, err)
		if string(pod.UID) != before.PodUID {
			t.Fatal("Pod replaced")
		}
		execInPod(t, ctx, cluster, before.PodName, "touch /workspace/repository/continue")
		result := waitDone(item)
		if actJob(result).Result.Outputs["answer"] != "real-act" || strings.Contains(logs(item), secret) || !strings.Contains(logs(item), "node24-post-ran") {
			t.Fatal("outputs or masking", result, logs(item))
		}
		warmSource := strings.Replace(source, "while [ ! -f /workspace/repository/continue ]; do sleep 1; done", "test '${{ steps.cache.outputs.cache-hit }}' = true", 1)
		warm := createCall(warmSource)
		waitDone(warm)
		failure := create(t, `on: push
jobs:
  bad:
    runs-on: actions
    steps:
      - run: exit 7
  downstream:
    needs: bad
    runs-on: actions
    steps:
      - run: echo MUST_NOT_RUN
  cleanup:
    needs: bad
    if: always()
    runs-on: homelab
    steps:
      - run: echo preserved-dag-cleanup
`)
		var failed run.Run
		waitForTimeout(t, ctx, "act failure propagation", 4*time.Minute, func() bool { failed = get(t, failure.ID); return failed.State.Terminal() })
		if jobByKey(t, failed, "bad").Result.Steps[0].ExitCode != 7 || jobByKey(t, failed, "bad").State != run.Failed || jobByKey(t, failed, "downstream").State != run.Skipped || jobByKey(t, failed, "cleanup").State != run.Succeeded {
			t.Fatal(failed)
		}
		none := create(t, `on: push
permissions: {}
jobs:
  none:
    runs-on: actions
    steps:
      - run: test -z '${{ secrets.GITHUB_TOKEN }}'
`)
		waitDone(none)
		timed := create(t, `on: push
jobs:
  deadline:
    runs-on: actions
    timeout-minutes: 1
    steps:
      - run: echo act-timeout-ready; sleep 120
`)
		waitForTimeout(t, ctx, "act job deadline", 4*time.Minute, func() bool { return get(t, timed.ID).State.Terminal() })
		if got := get(t, timed.ID); actJob(got).State != run.TimedOut {
			t.Fatal("act job deadline was not preserved", got, logs(timed))
		}
		cancelled := create(t, `on: push
jobs:
  active:
    runs-on: actions
    steps:
      - run: printf 'act-cancel-%s\n' ready; printf '%2048s\n' ''; sleep 120
`)
		waitForTimeout(t, ctx, "act cancellation checkpoint", 8*time.Minute, func() bool { return strings.Contains(logs(cancelled), "act-cancel-ready") })
		request(t, "POST", "/api/runs/"+cancelled.ID+"/cancel", nil, 200)
		waitFor(t, ctx, "act cancelled", func() bool { return get(t, cancelled.ID).State == run.Cancelled })
		waitFor(t, ctx, "all owned engines removed", func() bool {
			pods, err := client.CoreV1().Pods("forgelet").List(ctx, metav1.ListOptions{LabelSelector: "forgelet.dev/job"})
			return err == nil && len(pods.Items) == 0
		})
		// A bad configured digest must stop preparation, never run user steps or
		// silently pull the public alias from Docker Hub instead.
		cm, err = client.CoreV1().ConfigMaps("forgelet").Get(ctx, "forgelet-config", metav1.GetOptions{})
		must(t, err)
		at := strings.LastIndex(mirror, "@sha256:")
		badMirror := mirror[:at] + "@sha256:" + strings.Repeat("b", 64)
		cm.Data["forgelet.yaml"] = strings.ReplaceAll(cm.Data["forgelet.yaml"], mirror, badMirror)
		_, err = client.CoreV1().ConfigMaps("forgelet").Update(ctx, cm, metav1.UpdateOptions{})
		must(t, err)
		restartServer(t, ctx, client, httpClient, endpoint)
		denied := create(t, `on: push
jobs:
  denied:
    runs-on: actions
    steps:
      - run: echo MIRROR_WORKFLOW_MUST_NOT_RUN
`)
		waitForTimeout(t, ctx, "bad mirror fails before user code", 4*time.Minute, func() bool { return get(t, denied.ID).State.Terminal() })
		if got := get(t, denied.ID); actJob(got).State != run.Failed || strings.Contains(logs(denied), "MIRROR_WORKFLOW_MUST_NOT_RUN") {
			t.Fatal("mirror failure was bypassed", got, logs(denied))
		}

	})
}

func execInPod(t *testing.T, ctx context.Context, cluster testcontainers.Container, pod, command string) {
	t.Helper()
	code, out, err := cluster.Exec(ctx, []string{"kubectl", "-n", "forgelet", "exec", pod, "-c", "executor", "--", "sh", "-c", command})
	must(t, err)
	data, err := io.ReadAll(out)
	must(t, err)
	if code != 0 {
		t.Fatalf("execution pod command: %d %s", code, data)
	}
}

func ensureTestRegistry(t *testing.T, ctx context.Context, client *kubernetes.Clientset) {
	t.Helper()
	_, err := client.CoreV1().Pods("forgelet").Get(ctx, "test-registry", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		hash, err := bcrypt.GenerateFromPassword([]byte("integration-registry-password"), bcrypt.MinCost)
		must(t, err)
		_, err = client.CoreV1().Secrets("forgelet").Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "registry-htpasswd"}, Data: map[string][]byte{"htpasswd": append([]byte("test:"), hash...)}}, metav1.CreateOptions{})
		must(t, err)
		_, err = client.CoreV1().Pods("forgelet").Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "test-registry", Labels: map[string]string{"app": "test-registry"}}, Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "auth", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "registry-htpasswd"}}}}, Containers: []corev1.Container{{Name: "registry", Image: "registry:2.8.3", Env: []corev1.EnvVar{{Name: "REGISTRY_AUTH", Value: "htpasswd"}, {Name: "REGISTRY_AUTH_HTPASSWD_REALM", Value: "integration"}, {Name: "REGISTRY_AUTH_HTPASSWD_PATH", Value: "/auth/htpasswd"}}, VolumeMounts: []corev1.VolumeMount{{Name: "auth", MountPath: "/auth", ReadOnly: true}}, Ports: []corev1.ContainerPort{{ContainerPort: 5000}}}}}}, metav1.CreateOptions{})
		must(t, err)
		_, err = client.CoreV1().Services("forgelet").Create(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "test-registry"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeNodePort, Selector: map[string]string{"app": "test-registry"}, Ports: []corev1.ServicePort{{Port: 5000, NodePort: 30500, TargetPort: intstr.FromInt32(5000)}}}}, metav1.CreateOptions{})
		must(t, err)
	} else {
		must(t, err)
	}
	waitFor(t, ctx, "act test registry ready", func() bool {
		pod, err := client.CoreV1().Pods("forgelet").Get(ctx, "test-registry", metav1.GetOptions{})
		if err != nil {
			return false
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				return true
			}
		}
		return false
	})
}
