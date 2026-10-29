//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dockercontainer "github.com/moby/moby/api/types/container"
	dockerclient "github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/k3s"
	appsv1 "k8s.io/api/apps/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/shitamachi/forgelet/internal/run"
)

const token = "test-only-admin-token-not-for-production"

// TestK3sConcurrentExecution starts a disposable cluster; it never reads the user's kubeconfig.
func TestK3sConcurrentExecution(t *testing.T) {
	// The real >1 GiB cache case can consume 24 minutes by itself. Keep each
	// functional/job deadline unchanged while allowing the entire suite to finish.
	timeout := 80 * time.Minute
	if os.Getenv("FORGELET_TEST_BUILD_BUSINESS_DIR") != "" {
		timeout = 90 * time.Minute
	}
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	root, err := filepath.Abs("../..")
	must(t, err)

	tag := fmt.Sprintf("test-%d", time.Now().UnixNano())
	image := "forgelet-integration:" + tag
	buildArgs := map[string]*string{}
	if proxy := os.Getenv("FORGELET_TEST_GOPROXY"); proxy != "" {
		buildArgs["GOPROXY"] = &proxy
	}
	if mirror := os.Getenv("FORGELET_TEST_DEBIAN_MIRROR"); mirror != "" {
		buildArgs["DEBIAN_MIRROR"] = &mirror
	}
	imageContainer, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{FromDockerfile: testcontainers.FromDockerfile{Context: root, Repo: "forgelet-integration", Tag: tag, BuildArgs: buildArgs, KeepImage: os.Getenv("FORGELET_TEST_KEEP_IMAGE") == "1", BuildLogWriter: os.Stdout, BuildOptionsModifier: func(options *dockerclient.ImageBuildOptions) {
		options.Target = "integration-act"
	}}}, Started: false})
	if imageContainer != nil {
		testcontainers.CleanupContainer(t, imageContainer)
	}
	must(t, err)
	cluster, err := k3s.Run(ctx, "rancher/k3s:v1.34.4-k3s1", testcontainers.WithExposedPorts("30080/tcp", "30500/tcp"), testcontainers.WithCmdArgs("--disable=metrics-server"),
		// Bound only local-path PVC storage in this disposable container. Pressure
		// tests write real blocks without filling the Docker host or node root disk.
		testcontainers.WithHostConfigModifier(func(hc *dockercontainer.HostConfig) {
			// The k3s module installs /run tmpfs in its HostConfig modifier,
			// replacing WithTmpfs options; append our mount after that modifier.
			hc.Tmpfs[testStorageRoot] = "rw,size=256m"
		}))
	if cluster != nil {
		testcontainers.CleanupContainer(t, cluster)
	}
	must(t, err)
	images := append([]string{image}, strings.Fields(os.Getenv("FORGELET_TEST_IMAGES"))...)
	must(t, cluster.LoadImages(ctx, images...))
	kubeconfig, err := cluster.GetKubeConfig(ctx)
	must(t, err)
	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	must(t, err)
	client, err := kubernetes.NewForConfig(restConfig)
	must(t, err)
	t.Cleanup(func() {
		if t.Failed() {
			diagnosticCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			nodes, _ := client.CoreV1().Nodes().List(diagnosticCtx, metav1.ListOptions{})
			if nodes != nil {
				for _, node := range nodes.Items {
					t.Logf("node %s conditions=%v taints=%v", node.Name, node.Status.Conditions, node.Spec.Taints)
				}
			}
			systemPods, _ := client.CoreV1().Pods("kube-system").List(diagnosticCtx, metav1.ListOptions{})
			if systemPods != nil {
				for _, pod := range systemPods.Items {
					t.Logf("system pod %s phase=%s containers=%v", pod.Name, pod.Status.Phase, pod.Status.ContainerStatuses)
				}
			}
			events, _ := client.CoreV1().Events("").List(diagnosticCtx, metav1.ListOptions{Limit: 100})
			if events != nil {
				for _, event := range events.Items {
					t.Logf("event %s/%s %s: %s", event.Namespace, event.InvolvedObject.Name, event.Reason, event.Message)
				}
			}
			pods, _ := client.CoreV1().Pods("forgelet").List(diagnosticCtx, metav1.ListOptions{})
			if pods != nil {
				for _, p := range pods.Items {
					t.Logf("pod %s: phase=%s conditions=%v", p.Name, p.Status.Phase, p.Status.Conditions)
					data, err := client.CoreV1().Pods("forgelet").GetLogs(p.Name, &corev1.PodLogOptions{Container: p.Spec.Containers[0].Name}).DoRaw(diagnosticCtx)
					t.Logf("logs %s: %s (%v)", p.Name, data, err)
				}
			}
		}
	})
	// Kubelet may have started a slow external pull before LoadImages finished.
	// Recreate only unready system Pods whose exact images were preloaded.
	preloaded := map[string]bool{}
	for _, name := range images {
		preloaded[name] = true
	}
	pods, err := client.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{})
	must(t, err)
	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodPending || pod.DeletionTimestamp != nil {
			continue
		}
		loaded := len(pod.Spec.Containers) > 0
		for _, container := range pod.Spec.Containers {
			loaded = loaded && preloaded[container.Image]
		}
		if loaded {
			err := client.CoreV1().Pods("kube-system").Delete(ctx, pod.Name, metav1.DeleteOptions{})
			if !apierrors.IsNotFound(err) {
				must(t, err)
			}
		}
	}
	// The API can be ready while the pinned cluster's DNS/storage add-ons are
	// still pulling images. Do not spend a workflow's deadline on cluster boot.
	waitForTimeout(t, ctx, "cluster DNS and storage provisioner ready", 5*time.Minute, func() bool {
		for _, name := range []string{"coredns", "local-path-provisioner"} {
			deployment, err := client.AppsV1().Deployments("kube-system").Get(ctx, name, metav1.GetOptions{})
			if err != nil || deployment.Status.ReadyReplicas < 1 {
				return false
			}
		}
		return true
	})
	cert, key := testCertificate(t)
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(cert) {
		t.Fatal("test CA")
	}
	httpClient := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: caPool, ServerName: "forgelet.forgelet.svc"}}}
	defer httpClient.CloseIdleConnections()
	// Install the actual shipped manifests, replacing only local image/config, test credentials and service exposure.
	install(t, ctx, client, filepath.Join(root, "deploy/install.yaml"), image, cert, key)
	endpoint, err := cluster.PortEndpoint(ctx, "30080/tcp", "https")
	must(t, err)

	waitForTimeout(t, ctx, "server readiness", 10*time.Minute, func() bool {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/healthz", nil)
		must(t, err)
		resp, err := httpClient.Do(req)
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode == 200
	})
	request := func(t *testing.T, method, path string, body any, status int) []byte {
		return api(t, ctx, httpClient, endpoint, method, path, body, token, status)
	}
	create := func(t *testing.T, source string) run.Run {
		var item run.Run
		must(t, json.Unmarshal(request(t, "POST", "/api/runs", map[string]any{"repository": "test/project", "sha": strings.Repeat("a", 40), "ref": "refs/heads/main", "workflow": source}, 201), &item))
		return item
	}
	get := func(t *testing.T, id string) run.Run {
		var item run.Run
		must(t, json.Unmarshal(request(t, "GET", "/api/runs/"+id, nil, 200), &item))
		return item
	}
	testWorkflowCalls(t, ctx, cluster, client, httpClient, endpoint, root, request, get)
	t.Run("resolved needs plan stays below the claim limit", func(t *testing.T) {
		producer := func(key string) string {
			return fmt.Sprintf(`  %s:
    runs-on: homelab
    outputs:
      blob: '${{ steps.emit.outputs.blob }}'
    steps:
      - id: emit
        shell: sh
        run: |
          printf 'blob=' >> "$GITHUB_OUTPUT"
          head -c 61440 /dev/zero | tr '\000' y >> "$GITHUB_OUTPUT"
          printf '\n' >> "$GITHUB_OUTPUT"
`, key)
		}
		source := "on: push\njobs:\n" + producer("left") + producer("right") + fmt.Sprintf(`  join:
    needs: [left, right]
    runs-on: homelab
    continue-on-error: true
    steps:
      - run: |
          echo SHOULD_NOT_RUN
          #%s
  after:
    needs: join
    runs-on: homelab
    steps:
      - run: echo SHOULD_NOT_RUN
  independent:
    runs-on: homelab
    steps:
      - run: echo independent-ok
`, strings.Repeat("x", 850<<10))
		record := create(t, source)
		result := finishRun(t, ctx, get, record.ID)
		join := jobByKey(t, result, "join")
		if result.State != run.Failed || join.State != run.Failed || join.PodUID != "" || join.Result == nil || !strings.Contains(join.Result.Error, "resolved execution plan") {
			t.Fatalf("unclaimable plan started or failure was hidden: state=%s join=%+v", result.State, join.Result)
		}
		if jobByKey(t, result, "after").State != run.Skipped || jobByKey(t, result, "independent").State != run.Succeeded {
			t.Fatal("oversized plan blocked failure propagation or independent execution")
		}
	})
	t.Run("workspace belongs to executor and Git accepts root checkout", func(t *testing.T) {
		item := create(t, `on: push
jobs:
  workspace:
    runs-on: homelab
    steps:
      - run: |
          test "$(stat -c %u "$GITHUB_WORKSPACE")" = "$(id -u)"
          test "$PWD" = "$GITHUB_WORKSPACE"
          git init --quiet
          git -c user.name=Test -c user.email=test@example.invalid commit --allow-empty --quiet -m initial
          git rev-parse --verify HEAD
          test -z "$(git status --porcelain)"
`)
		if result := finishRun(t, ctx, get, item.ID); result.State != run.Succeeded {
			t.Fatalf("root workspace Git operations failed: %+v", result)
		}
	})
	t.Run("multiple jobs obey limit and share step state", func(t *testing.T) {
		var source strings.Builder
		source.WriteString("name: parallel\non: push\njobs:\n")
		for i := 0; i < 5; i++ {
			fmt.Fprintf(&source, "  job%d:\n    runs-on: homelab\n    steps:\n      - id: first\n        shell: sh\n        run: |\n          echo shared > file.txt\n          echo GREETING=hello >> \"$GITHUB_ENV\"\n          echo result=passed >> \"$GITHUB_OUTPUT\"\n          sleep 3\n      - shell: sh\n        run: |\n          test \"$GREETING\" = hello\n          test \"${{ steps.first.outputs.result }}\" = passed\n          cat file.txt\n", i)
		}
		item := create(t, source.String())
		seenParallel := false
		seenQueued := false
		waitFor(t, ctx, "five concurrent jobs finish", func() bool {
			current := get(t, item.ID)
			active := 0
			queued := 0
			for _, j := range current.Jobs {
				if j.State == run.Starting || j.State == run.Running {
					active++
				}
				if j.State == run.Queued {
					queued++
				}
			}
			if active > 2 {
				t.Fatalf("concurrency limit exceeded: %d", active)
			}
			seenParallel = seenParallel || active == 2
			seenQueued = seenQueued || queued > 0
			if current.State.Terminal() {
				if current.State != run.Succeeded {
					detail, _ := json.MarshalIndent(current, "", "  ")
					t.Fatalf("run failed: %s", detail)
				}
				return true
			}
			return false
		})
		if !seenParallel || !seenQueued {
			t.Fatal("did not observe parallel execution and queued overflow")
		}
		for _, job := range get(t, item.ID).Jobs {
			var entries []run.Log
			must(t, json.Unmarshal(request(t, "GET", "/api/jobs/"+job.ID+"/logs", nil, 200), &entries))
			var out strings.Builder
			for _, entry := range entries {
				out.WriteString(entry.Data)
			}
			if !strings.Contains(out.String(), "shared") {
				t.Fatalf("missing durable output: %q", out.String())
			}
		}
	})
	t.Run("dependency conditions and failure propagation", func(t *testing.T) {
		source, err := os.ReadFile(filepath.Join(root, "test/fixtures/conditions.yml"))
		must(t, err)
		item := create(t, string(source))
		var finished run.Run
		waitFor(t, ctx, "dependency graph completes", func() bool {
			finished = get(t, item.ID)
			active := 0
			for _, job := range finished.Jobs {
				if job.State == run.Starting || job.State == run.Running {
					active++
					for _, dependency := range job.Plan.Needs {
						if !jobByKey(t, finished, dependency).State.Terminal() {
							t.Fatalf("%s ran before %s completed", job.Plan.Key, dependency)
						}
					}
				}
			}
			if active > 2 {
				t.Fatalf("DAG exceeded concurrency limit: %d", active)
			}
			return finished.State.Terminal()
		})
		if finished.State != run.Failed {
			t.Fatalf("cleanup incorrectly cleared workflow failure: %s", finished.State)
		}
		want := map[string]run.State{"seed": run.Succeeded, "build": run.Failed, "blocked": run.Skipped, "transitive": run.Succeeded, "report": run.Succeeded, "success_path": run.Succeeded, "false_job": run.Skipped, "not_after_skip": run.Skipped, "always_after_skip": run.Succeeded}
		for key, state := range want {
			job := jobByKey(t, finished, key)
			if job.State != state {
				detail, _ := json.MarshalIndent(job, "", "  ")
				t.Fatalf("job %s expected %s: %s", key, state, detail)
			}
			if state == run.Skipped && job.PodUID != "" {
				t.Fatalf("skipped %s created a Pod", key)
			}
		}
		build := jobByKey(t, finished, "build")
		steps := build.Result.Steps
		if len(steps) != 5 || steps[0].Outcome != run.Failed || steps[0].State != run.Succeeded || steps[2].State != run.Skipped || steps[4].State != run.Succeeded {
			t.Fatalf("step conditions were not preserved: %+v", steps)
		}
		for _, key := range []string{"build", "report"} {
			job := jobByKey(t, finished, key)
			var logs []run.Log
			must(t, json.Unmarshal(request(t, "GET", "/api/jobs/"+job.ID+"/logs", nil, 200), &logs))
			text, _ := json.Marshal(logs)
			if strings.Contains(string(text), "SHOULD_NOT_RUN") {
				t.Fatal("default-skipped step executed")
			}
			if key == "report" && !strings.Contains(string(text), "report-ready") {
				t.Fatalf("dependent output not used: %s", text)
			}
		}
	})
	t.Run("workload identity isolation and cancellation", func(t *testing.T) {
		item := create(t, "on: push\njobs:\n  first:\n    runs-on: homelab\n    steps:\n      - shell: sh\n        run: sleep 30\n  second:\n    runs-on: homelab\n    steps:\n      - shell: sh\n        run: sleep 30\n  third:\n    runs-on: homelab\n    steps:\n      - shell: sh\n        run: sleep 30\n")
		var current run.Run
		waitFor(t, ctx, "two running pods", func() bool {
			current = get(t, item.ID)
			n := 0
			for _, j := range current.Jobs {
				if j.State == run.Running {
					n++
				}
			}
			return n == 2
		})
		var active, other run.Job
		for _, j := range current.Jobs {
			if j.State == run.Running {
				active = j
			} else {
				other = j
			}
		}
		scoped, err := client.CoreV1().ServiceAccounts("forgelet").CreateToken(ctx, "forgelet-executor", &authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{Audiences: []string{"forgelet"}, BoundObjectRef: &authenticationv1.BoundObjectReference{Kind: "Pod", APIVersion: "v1", Name: active.PodName, UID: types.UID(active.PodUID)}}}, metav1.CreateOptions{})
		must(t, err)
		api(t, ctx, httpClient, endpoint, "POST", "/internal/jobs/"+other.ID+"/claim", nil, scoped.Status.Token, 401)
		restricted := *restConfig
		restricted.CertData = nil
		restricted.KeyData = nil
		restricted.CertFile = ""
		restricted.KeyFile = ""
		restricted.BearerToken = scoped.Status.Token
		restricted.BearerTokenFile = ""
		unprivileged, err := kubernetes.NewForConfig(&restricted)
		must(t, err)
		if _, err = unprivileged.CoreV1().Pods("forgelet").List(ctx, metav1.ListOptions{}); err == nil {
			t.Fatal("executor acquired Kubernetes API access")
		}
		request(t, "POST", "/api/runs/"+item.ID+"/cancel", nil, 200)
		waitFor(t, ctx, "all jobs cancelled", func() bool {
			current = get(t, item.ID)
			if !current.State.Terminal() {
				return false
			}
			for _, j := range current.Jobs {
				if j.State != run.Cancelled {
					t.Fatalf("job not cancelled: %+v", j)
				}
				if len(j.Plan.Needs) == 0 && (j.Result == nil || j.Result.Error != "") {
					t.Fatalf("executor cancellation flush was not acknowledged: %+v", j.Result)
				}
			}
			return true
		})
	})
	t.Run("server restart keeps active attempt and dependent jobs", func(t *testing.T) {
		item := create(t, "on: push\njobs:\n  restart:\n    runs-on: homelab\n    steps:\n      - shell: sh\n        run: |\n          echo before-restart\n          while [ ! -f /workspace/control-plane-recovered ]; do sleep 1; done\n          echo after-restart\n  dependent:\n    needs: restart\n    runs-on: homelab\n    steps:\n      - shell: sh\n        run: echo dependency-finished\n")
		var uid, podName string
		waitFor(t, ctx, "attempt starts", func() bool {
			current := get(t, item.ID)
			if jobByKey(t, current, "restart").State == run.Running {
				uid = jobByKey(t, current, "restart").PodUID
				podName = jobByKey(t, current, "restart").PodName
				if jobByKey(t, current, "dependent").State != run.Queued {
					t.Fatal("dependent job started before dependency completed")
				}
				return true
			}
			return false
		})
		scale, err := client.AppsV1().Deployments("forgelet").GetScale(ctx, "forgelet", metav1.GetOptions{})
		must(t, err)
		scale.Spec.Replicas = 0
		_, err = client.AppsV1().Deployments("forgelet").UpdateScale(ctx, "forgelet", scale, metav1.UpdateOptions{})
		must(t, err)
		defer func() {
			cleanupCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer done()
			current, e := client.AppsV1().Deployments("forgelet").GetScale(cleanupCtx, "forgelet", metav1.GetOptions{})
			if e != nil {
				return
			}
			current.Spec.Replicas = 1
			_, _ = client.AppsV1().Deployments("forgelet").UpdateScale(cleanupCtx, "forgelet", current, metav1.UpdateOptions{})
		}()
		waitFor(t, ctx, "control plane stopped", func() bool {
			ps, e := client.CoreV1().Pods("forgelet").List(ctx, metav1.ListOptions{LabelSelector: "app=forgelet"})
			return e == nil && len(ps.Items) == 0
		})
		// Exceeds the former one-minute polling limit. Keep the shell alive
		// without producing output until the control plane is back.
		timer := time.NewTimer(70 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			t.Fatal(ctx.Err())
		case <-timer.C:
		}
		pod, err := client.CoreV1().Pods("forgelet").Get(ctx, podName, metav1.GetOptions{})
		must(t, err)
		if string(pod.UID) != uid || pod.Status.Phase != corev1.PodRunning {
			t.Fatalf("attempt died during outage: %+v", pod.Status)
		}
		scale, err = client.AppsV1().Deployments("forgelet").GetScale(ctx, "forgelet", metav1.GetOptions{})
		must(t, err)
		scale.Spec.Replicas = 1
		_, err = client.AppsV1().Deployments("forgelet").UpdateScale(ctx, "forgelet", scale, metav1.UpdateOptions{})
		must(t, err)
		waitFor(t, ctx, "control plane restored", func() bool { return probeStatus(t, ctx, httpClient, endpoint+"/healthz") == 200 })
		clusterCommand(t, ctx, cluster, "kubectl", "-n", "forgelet", "exec", podName, "--", "touch", "/workspace/control-plane-recovered")
		waitFor(t, ctx, "run recovers", func() bool {
			current := get(t, item.ID)
			if jobByKey(t, current, "restart").PodUID != uid {
				t.Fatal("attempt pod changed after control-plane restart")
			}
			if current.State.Terminal() {
				if current.State != run.Succeeded {
					detail, _ := json.MarshalIndent(current, "", "  ")
					t.Fatalf("restart run failed: %s", detail)
				}
				return true
			}
			return false
		})
		request(t, "POST", "/api/backup", nil, 201)
	})
	t.Run("ownership drift interrupts without deleting a foreign pod", func(t *testing.T) {
		item := create(t, "on: push\njobs:\n  identity:\n    runs-on: homelab\n    steps:\n      - shell: sh\n        run: sleep 90\n")
		var job run.Job
		waitFor(t, ctx, "identity job running", func() bool { job = get(t, item.ID).Jobs[0]; return job.State == run.Running })
		pod, err := client.CoreV1().Pods("forgelet").Get(ctx, job.PodName, metav1.GetOptions{})
		must(t, err)
		pod.Labels["forgelet.dev/job"] = "foreign-job"
		_, err = client.CoreV1().Pods("forgelet").Update(ctx, pod, metav1.UpdateOptions{})
		must(t, err)
		defer func() {
			cleanupCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer done()
			zero := int64(0)
			uid := types.UID(job.PodUID)
			_ = client.CoreV1().Pods("forgelet").Delete(cleanupCtx, job.PodName, metav1.DeleteOptions{GracePeriodSeconds: &zero, Preconditions: &metav1.Preconditions{UID: &uid}})
		}()
		waitFor(t, ctx, "identity mismatch recorded", func() bool { return get(t, item.ID).Jobs[0].State == run.Interrupted })
		pod, err = client.CoreV1().Pods("forgelet").Get(ctx, job.PodName, metav1.GetOptions{})
		must(t, err)
		if pod.DeletionTimestamp != nil || string(pod.UID) != job.PodUID {
			t.Fatal("foreign Pod deleted or replaced")
		}
	})
	t.Run("lost executor is interrupted without replay", func(t *testing.T) {
		item := create(t, "on: push\njobs:\n  lost:\n    runs-on: homelab\n    steps:\n      - shell: sh\n        run: sleep 30\n")
		var started run.Job
		waitFor(t, ctx, "executor running", func() bool {
			started = get(t, item.ID).Jobs[0]
			return started.State == run.Running
		})
		zero := int64(0)
		must(t, client.CoreV1().Pods("forgelet").Delete(ctx, started.PodName, metav1.DeleteOptions{GracePeriodSeconds: &zero}))
		waitFor(t, ctx, "lost executor interrupted", func() bool {
			current := get(t, item.ID)
			if !current.State.Terminal() {
				return false
			}
			job := current.Jobs[0]
			if job.State != run.Interrupted || job.PodUID != started.PodUID {
				t.Fatalf("lost attempt was replayed or incorrectly reported: %+v", job)
			}
			return true
		})
	})
	testDiskPressure(t, ctx, cluster, client, httpClient, endpoint, request, create, get)
	testPhaseThree(t, ctx, cluster, client, httpClient, endpoint, root, request, create, get)
	testBatchedLogRestart(t, ctx, cluster, client, httpClient, endpoint, create, get, request)
	testActJobs(t, ctx, cluster, client, httpClient, endpoint, image, request, create, get)
	testRustProfile(t, ctx, cluster, client, httpClient, endpoint, image, request, get)
	testBuildxCache(t, ctx, cluster, client, httpClient, endpoint, image, request, create, get)
	testBusinessWorkflows(t, ctx, cluster, client, httpClient, endpoint, image, root, request, get)
	testBuildBusinessWorkflows(t, ctx, cluster, client, httpClient, endpoint, image, root, request, get)
	testPhaseTwo(t, ctx, cluster, client, httpClient, endpoint, root, request, create, get)
	testWebConsole(t, ctx, client, httpClient, endpoint, root)
	testAuthentication(t, ctx, cluster, client, httpClient, endpoint, root)
	t.Run("retention expires logs then history and keeps active run", func(t *testing.T) {
		cm, err := client.CoreV1().ConfigMaps("forgelet").Get(ctx, "forgelet-config", metav1.GetOptions{})
		must(t, err)
		config := cm.Data["forgelet.yaml"]
		for _, pair := range [][2]string{{"history: 720h", "history: 12s"}, {"logs: 168h", "logs: 3s"}, {"events: 720h", "events: 12s"}, {"backups: 168h", "backups: 3s"}, {"interval: 10m", "interval: 1s"}} {
			config = strings.ReplaceAll(config, pair[0], pair[1])
		}
		cm.Data["forgelet.yaml"] = config
		_, err = client.CoreV1().ConfigMaps("forgelet").Update(ctx, cm, metav1.UpdateOptions{})
		must(t, err)
		restartServer(t, ctx, client, httpClient, endpoint)
		live := create(t, "on: push\njobs:\n  active:\n    runs-on: homelab\n    steps:\n      - shell: sh\n        run: |\n          echo retain-active-log\n          sleep 90\n")
		old := create(t, "on: push\njobs:\n  old:\n    runs-on: homelab\n    steps:\n      - shell: sh\n        run: echo expire-this-log\n")
		waitFor(t, ctx, "short run finishes", func() bool { return get(t, old.ID).State == run.Succeeded })
		logURL := endpoint + "/api/jobs/" + old.Jobs[0].ID + "/logs"
		waitFor(t, ctx, "logs return gone", func() bool { return probeStatus(t, ctx, httpClient, logURL) == http.StatusGone })
		retained := get(t, old.ID)
		if !retained.Jobs[0].LogsExpired || retained.Jobs[0].Result == nil {
			t.Fatalf("log cleanup erased result or failed to mark expiry: %+v", retained)
		}
		waitFor(t, ctx, "history expires", func() bool {
			return probeStatus(t, ctx, httpClient, endpoint+"/api/runs/"+old.ID) == http.StatusNotFound
		})
		current := get(t, live.ID)
		if current.State != run.Running || current.Jobs[0].LogsExpired {
			t.Fatalf("cleanup affected active run: %+v", current)
		}
		logs := request(t, "GET", "/api/jobs/"+current.Jobs[0].ID+"/logs", nil, 200)
		if !strings.Contains(string(logs), "retain-active-log") {
			t.Fatalf("active log missing: %s", logs)
		}
		request(t, "POST", "/api/maintenance", nil, 200)
		request(t, "POST", "/api/runs/"+live.ID+"/cancel", nil, 200)
		waitFor(t, ctx, "active run cancels", func() bool { return get(t, live.ID).State == run.Cancelled })
	})
	waitFor(t, ctx, "execution pod cleanup", func() bool {
		ps, err := client.CoreV1().Pods("forgelet").List(ctx, metav1.ListOptions{LabelSelector: "forgelet.dev/job"})
		return err == nil && len(ps.Items) == 0
	})
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func waitFor(t *testing.T, ctx context.Context, name string, predicate func() bool) {
	t.Helper()
	waitForTimeout(t, ctx, name, 150*time.Second, predicate)
}
func waitForTimeout(t *testing.T, ctx context.Context, name string, timeout time.Duration, predicate func() bool) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if predicate() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s: %v", name, ctx.Err())
		case <-deadline.C:
			t.Fatalf("timed out: %s", name)
		case <-ticker.C:
		}
	}
}
func api(t *testing.T, ctx context.Context, client *http.Client, endpoint, method, path string, body any, credential string, status int) []byte {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		must(t, err)
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint+path, reader)
	must(t, err)
	req.Header.Set("Authorization", "Bearer "+credential)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	// A ready replacement Pod may briefly precede Service endpoint propagation.
	// Only retry transport failures on reads; mutations must remain single requests.
	until := time.Now().Add(10 * time.Second)
	for err != nil && method == http.MethodGet && time.Now().Before(until) {
		client.CloseIdleConnections()
		select {
		case <-ctx.Done():
			must(t, ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
		resp, err = client.Do(req)
	}
	must(t, err)
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	must(t, err)
	if resp.StatusCode != status {
		t.Fatalf("%s %s: got %d want %d: %s", method, path, resp.StatusCode, status, data)
	}
	return data
}

func testCertificate(t *testing.T) ([]byte, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	must(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "forgelet test CA"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true, DNSNames: []string{"forgelet", "forgelet.forgelet.svc", "localhost", "git.forgelet.svc"}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	must(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

func install(t *testing.T, ctx context.Context, client *kubernetes.Clientset, path, image string, cert, key []byte) {
	t.Helper()
	data, err := os.ReadFile(path)
	must(t, err)
	decoder := yaml.NewYAMLToJSONDecoder(bytes.NewReader(data))
	codec := serializer.NewCodecFactory(scheme.Scheme).UniversalDeserializer()
	for {
		var raw json.RawMessage
		err = decoder.Decode(&raw)
		if errors.Is(err, io.EOF) {
			break
		}
		must(t, err)
		obj, _, err := codec.Decode(raw, nil, nil)
		must(t, err)
		switch v := obj.(type) {
		case *corev1.Namespace:
			_, err = client.CoreV1().Namespaces().Create(ctx, v, metav1.CreateOptions{})
			must(t, err)
			for _, secret := range []*corev1.Secret{{ObjectMeta: metav1.ObjectMeta{Name: "forgelet-github", Namespace: "forgelet"}, Data: map[string][]byte{"app.pem": key}}, {ObjectMeta: metav1.ObjectMeta{Name: "forgelet-encryption", Namespace: "forgelet"}, Data: map[string][]byte{"key": bytes.Repeat([]byte{17}, 32)}}, {ObjectMeta: metav1.ObjectMeta{Name: "forgelet-auth", Namespace: "forgelet"}, StringData: map[string]string{"admin-token": token, "webhook-secret": "integration-webhook-secret"}}, {ObjectMeta: metav1.ObjectMeta{Name: "forgelet-api-tls", Namespace: "forgelet"}, Data: map[string][]byte{"tls.crt": cert, "tls.key": key}}, {ObjectMeta: metav1.ObjectMeta{Name: "forgelet-api-ca", Namespace: "forgelet"}, Data: map[string][]byte{"ca.crt": cert}}} {
				_, err = client.CoreV1().Secrets("forgelet").Create(ctx, secret, metav1.CreateOptions{})
				must(t, err)
			}
			registryAuth, encodeErr := json.Marshal(map[string]any{"auths": map[string]any{strings.SplitN(registryImage(t, ctx, client), "/", 2)[0]: map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte("test:integration-registry-password"))}}})
			must(t, encodeErr)
			_, err = client.CoreV1().Secrets("forgelet").Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "forgelet-registry"}, Type: corev1.SecretTypeDockerConfigJson, Data: map[string][]byte{corev1.DockerConfigJsonKey: registryAuth}}, metav1.CreateOptions{})
			must(t, err)
		case *corev1.ServiceAccount:
			_, err = client.CoreV1().ServiceAccounts("forgelet").Create(ctx, v, metav1.CreateOptions{})
		case *rbacv1.Role:
			_, err = client.RbacV1().Roles("forgelet").Create(ctx, v, metav1.CreateOptions{})
		case *rbacv1.RoleBinding:
			_, err = client.RbacV1().RoleBindings("forgelet").Create(ctx, v, metav1.CreateOptions{})
		case *rbacv1.ClusterRole:
			_, err = client.RbacV1().ClusterRoles().Create(ctx, v, metav1.CreateOptions{})
		case *rbacv1.ClusterRoleBinding:
			_, err = client.RbacV1().ClusterRoleBindings().Create(ctx, v, metav1.CreateOptions{})
		case *corev1.ConfigMap:
			// Exercise legacy token access first; the console subtest switches to open.
			v.Data["forgelet.yaml"] = strings.Replace(v.Data["forgelet.yaml"], "admin_access: session", "admin_access: token", 1)
			v.Data["forgelet.yaml"] = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(v.Data["forgelet.yaml"], "forgelet-executor:dev", image), "max_parallel: 4", "max_parallel: 2"), "example/project", "test/project")
			v.Data["forgelet.yaml"] = strings.ReplaceAll(strings.ReplaceAll(v.Data["forgelet.yaml"], "pause_below_bytes: 536870912", "pause_below_bytes: 33554432"), "resume_at_bytes: 1073741824", "resume_at_bytes: 67108864")
			v.Data["forgelet.yaml"] = strings.Replace(v.Data["forgelet.yaml"], "secret_key_file: ''", "secret_key_file: /encryption/key", 1)
			v.Data["forgelet.yaml"] = strings.ReplaceAll(strings.ReplaceAll(v.Data["forgelet.yaml"], "github_app_id: ''", "github_app_id: '7'"), "github_app_key_file: ''", "github_app_key_file: /github/app.pem\ngithub_api_url: https://localhost:9443")
			script, readErr := os.ReadFile(filepath.Join(filepath.Dir(path), "../test/fixtures/github.js"))
			must(t, readErr)
			v.Data["github.js"] = string(script)
			v.Data["forgelet.yaml"] = strings.Replace(v.Data["forgelet.yaml"], "profiles:\n", fmt.Sprintf("profiles:\n  builder:\n    image: %s\n    build:\n      images: [%s]\n      insecure_registry: true\n      registry_secret: forgelet-registry\n", image, registryImage(t, ctx, client)), 1)

			_, configErr := yaml.ToJSON([]byte(v.Data["forgelet.yaml"]))
			must(t, configErr)
			_, err = client.CoreV1().ConfigMaps("forgelet").Create(ctx, v, metav1.CreateOptions{})
		case *corev1.PersistentVolumeClaim:
			_, err = client.CoreV1().PersistentVolumeClaims("forgelet").Create(ctx, v, metav1.CreateOptions{})
		case *appsv1.Deployment:
			v.Spec.Template.Spec.Containers[0].Image = image
			v.Spec.Template.Spec.Containers[0].Env = append(v.Spec.Template.Spec.Containers[0].Env, corev1.EnvVar{Name: "SSL_CERT_FILE", Value: "/tls/tls.crt"})
			v.Spec.Template.Spec.Containers = append(v.Spec.Template.Spec.Containers, corev1.Container{Name: "github-test-api", Image: image, Command: []string{"/opt/forgelet/node24/bin/node", "/config/github.js"}, VolumeMounts: []corev1.VolumeMount{{Name: "tls", MountPath: "/tls", ReadOnly: true}, {Name: "config", MountPath: "/config", ReadOnly: true}, {Name: "data", MountPath: "/fixture-state"}}})
			_, err = client.AppsV1().Deployments("forgelet").Create(ctx, v, metav1.CreateOptions{})
		case *corev1.Service:
			v.Spec.Type = corev1.ServiceTypeNodePort
			v.Spec.Ports[0].NodePort = 30080
			v.Spec.Ports[0].TargetPort = intstr.FromInt32(8443)
			_, err = client.CoreV1().Services("forgelet").Create(ctx, v, metav1.CreateOptions{})
		default:
			t.Fatalf("unhandled installation object %T", v)
		}
		must(t, err)
	}
}

func jobByKey(t *testing.T, item run.Run, key string) run.Job {
	t.Helper()
	for _, job := range item.Jobs {
		if job.Plan.Key == key {
			return job
		}
	}
	t.Fatalf("job %s missing from run %s", key, item.ID)
	return run.Job{}
}

func restartServer(t *testing.T, ctx context.Context, client *kubernetes.Clientset, httpClient *http.Client, endpoint string) {
	t.Helper()
	var old string
	waitFor(t, ctx, "one live server before restart", func() bool {
		pods, err := client.CoreV1().Pods("forgelet").List(ctx, metav1.ListOptions{LabelSelector: "app=forgelet"})
		if err != nil {
			return false
		}
		live := []string{}
		for _, p := range pods.Items {
			if p.DeletionTimestamp == nil {
				live = append(live, p.Name)
			}
		}
		if len(live) != 1 {
			return false
		}
		old = live[0]
		return true
	})
	must(t, client.CoreV1().Pods("forgelet").Delete(ctx, old, metav1.DeleteOptions{}))
	waitFor(t, ctx, "replacement server ready", func() bool {
		ps, err := client.CoreV1().Pods("forgelet").List(ctx, metav1.ListOptions{LabelSelector: "app=forgelet"})
		if err != nil {
			return false
		}
		for _, p := range ps.Items {
			if p.Name == old || p.DeletionTimestamp != nil {
				continue
			}
			for _, c := range p.Status.Conditions {
				if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
					return true
				}
			}
		}
		return false
	})
	httpClient.CloseIdleConnections()
	api(t, ctx, httpClient, endpoint, "GET", "/healthz", nil, token, http.StatusOK)
}

func probeStatus(t *testing.T, ctx context.Context, client *http.Client, url string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	must(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 2<<20))
	return resp.StatusCode
}
