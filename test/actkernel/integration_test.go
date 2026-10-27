//go:build integration

package actkernel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/k3s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func TestCandidateBehavior(t *testing.T) {
	for _, candidate := range []Candidate{Upstream, Gitea} {
		t.Run(string(candidate), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
			defer cancel()
			dir := t.TempDir()
			if err := Prepare(dir, candidate); err != nil {
				t.Fatal(err)
			}
			// Stock HostEnvironment has a separately recorded PTY race in both
			// candidates. Run stock behavior plus race checks of our own boundary;
			// this is not a claim that either stock host runtime is race-clean.
			for _, args := range [][]string{{"test", "-mod=readonly", "-count=1", "-v", "."}, {"test", "-mod=readonly", "-race", "-count=1", "-run", "TestLogBoundary|TestInvalidRequest", "."}} {
				cmd := exec.CommandContext(ctx, "go", args...)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "GOWORK=off")
				data, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("candidate behavior: %v\n%s", err, data)
				}
				t.Logf("%s", data)
			}
		})
	}
}

// TestKernelPods exercises stock library builds in disposable k3s Pods. There
// is no host Docker socket, hostPath, service-account token or user kubeconfig.
func TestKernelPods(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Minute)
	defer cancel()
	root := t.TempDir()
	for _, candidate := range []Candidate{Upstream, Gitea} {
		dir := filepath.Join(root, string(candidate))
		if err := Prepare(dir, candidate); err != nil {
			t.Fatal(err)
		}
		if err := Build(ctx, dir, filepath.Join(root, "worker-"+string(candidate)), "linux", runtime.GOARCH); err != nil {
			t.Fatal(err)
		}
	}
	if err := PrepareActionSources(ctx, filepath.Join(root, "action-sources")); err != nil {
		t.Fatal(err)
	}
	dockerfile := `FROM docker:28.5.1-cli AS docker
FROM node:20.20.0-bookworm-slim AS node20
FROM node:24.14.0-bookworm-slim
COPY --from=golang:1.27.3-alpine /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=docker /usr/local/bin/docker /usr/local/bin/docker
COPY --from=docker /usr/local/libexec/docker/cli-plugins /usr/local/libexec/docker/cli-plugins
COPY --from=node20 /usr/local/bin/node /opt/probe/node20/node
RUN sed -i 's|http://deb.debian.org|https://deb.debian.org|g' /etc/apt/sources.list.d/debian.sources && apt-get update && apt-get install -y --no-install-recommends git bash ca-certificates curl tar xz-utils unzip zstd gcc g++ make && rm -rf /var/lib/apt/lists/*
COPY /worker-upstream /worker-gitea /usr/local/bin/
COPY /action-sources /opt/probe/action-sources
`
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte(dockerfile), 0o600); err != nil {
		t.Fatal(err)
	}
	image := os.Getenv("FORGELET_KERNEL_IMAGE")
	if image == "" {
		image = fmt.Sprintf("forgelet-kernel:probe-%d", time.Now().UnixNano())
		build, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{FromDockerfile: testcontainers.FromDockerfile{Context: root, Repo: "forgelet-kernel", Tag: strings.TrimPrefix(image, "forgelet-kernel:"), KeepImage: os.Getenv("FORGELET_KERNEL_KEEP_IMAGE") == "1", BuildLogWriter: os.Stdout}}})
		if build != nil {
			testcontainers.CleanupContainer(t, build)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	cluster, err := k3s.Run(ctx, "rancher/k3s:v1.34.4-k3s1", testcontainers.WithCmdArgs("--disable=metrics-server", "--disable=traefik"))
	if cluster != nil {
		testcontainers.CleanupContainer(t, cluster)
	}
	if err != nil {
		t.Fatal(err)
	}
	images := []string{image, "docker:28.5.1-dind", "rancher/mirrored-coredns-coredns:1.14.1", "rancher/local-path-provisioner:v0.0.34", "rancher/mirrored-library-busybox:1.37.0"}
	for _, image := range images[1:] {
		// Pull missing infrastructure images on a fresh development machine.
		// These preparatory containers are never started.
		prepared, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: image}})
		if prepared != nil {
			testcontainers.CleanupContainer(t, prepared)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := cluster.LoadImages(ctx, images...); err != nil {
		t.Fatal(err)
	}
	kubeconfig, err := cluster.GetKubeConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kernel"}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []Candidate{Upstream, Gitea} {
		t.Run(string(candidate), func(t *testing.T) {
			for _, scenario := range []string{"lifecycle", "failure", "cancel", "workflow", "workflow-failure", "cache", "docker", "build-push"} {
				name := scenario
				if candidate == Gitea && scenario == "build-push" {
					name = "build-push_github_api_gap"
				}
				t.Run(name, func(t *testing.T) {
					files, workflow, state, docker := kernelFixture(scenario)
					if candidate == Gitea && scenario == "build-push" {
						state = "failure"
					}
					files["workflow.yml"] = workflow
					req := map[string]any{"mode": "job", "workdir": "/workspace", "workflow": "workflow.yml", "cache_dir": "/cache", "job_id": "only", "timeout_seconds": 60, "secrets": map[string]string{"TOKEN": "fixture-secret-never-in-logs"}}
					req["docker"] = docker
					req["action_sources"] = "/opt/probe/action-sources"
					if scenario == "cancel" {
						req["timeout_seconds"] = 2
					}
					if scenario == "cache" {
						req["cache"] = true
						req["timeout_seconds"] = 180
					}
					if scenario == "build-push" {
						req["timeout_seconds"] = 180
						req["secrets"] = map[string]string{"TOKEN": "fixture-secret-never-in-logs", "GITHUB_TOKEN": "fixture-github-api-token"}
						req["env"] = map[string]string{"GITHUB_API_URL": "http://127.0.0.1:18081"}
					}
					if strings.HasPrefix(scenario, "workflow") {
						req["mode"] = "workflow"
					}
					logs := runKernelPod(t, ctx, client, image, candidate, files, req, docker)
					if !strings.Contains(logs, `"state":"`+state+`"`) || strings.Contains(logs, "fixture-secret-never-in-logs") {
						t.Fatalf("state or masking failed:\n%s", logs)
					}
					if scenario == "lifecycle" && (!strings.Contains(logs, `"answer":"42"`) || !strings.Contains(logs, "POST_OK")) {
						t.Fatalf("lifecycle outputs/post missing:\n%s", logs)
					}
					if scenario == "failure" && (!strings.Contains(logs, "POST_OK") || !strings.Contains(logs, "CLEANUP_OK")) {
						t.Fatalf("failure cleanup missing:\n%s", logs)
					}
					if scenario == "docker" && !strings.Contains(logs, "DOCKER_ACTION_OK") {
						t.Fatalf("Docker action did not run:\n%s", logs)
					}
					if scenario == "workflow" && !strings.Contains(logs, "WORKFLOW_OUTPUT_42") {
						t.Fatalf("reusable workflow output missing:\n%s", logs)
					}
					if scenario == "workflow-failure" {
						if strings.Contains(logs, "FORBIDDEN_EXECUTED") {
							t.Fatalf("reusable workflow failure did not propagate:\n%s", logs)
						}
						// This probe measures the stock full-workflow scheduler too;
						// forgelet must retain its own durable scheduler either way.
						t.Logf("stock full-workflow engine ran always cleanup: %t", strings.Contains(logs, "WORKFLOW_CLEANUP"))
					}
					if scenario == "cache" && (strings.Count(logs, `"state":"success"`) != 2 || !strings.Contains(logs, `"hit":"true"`)) {
						t.Fatalf("cache did not survive cold -> warm execution:\n%s", logs)
					}
					if scenario == "build-push" {
						if candidate == Gitea {
							if !strings.Contains(logs, "API_ENDPOINT_MISMATCH") || strings.Contains(logs, "REGISTRY_IMAGE_VERIFIED") {
								t.Fatalf("stock Gitea GitHub API compatibility changed; reassess the documented gap:\n%s", logs)
							}
							t.Log("known incompatibility reproduced: stock Gitea overwrites GITHUB_API_URL with GitHub /api/v1; build-push workflow is blocked")
						} else if !strings.Contains(logs, "REGISTRY_IMAGE_VERIFIED") {
							t.Fatalf("real Docker Actions did not publish a valid image:\n%s", logs)
						}
					}
				})
			}
		})
	}
}

func runKernelPod(t *testing.T, ctx context.Context, client *kubernetes.Clientset, image string, candidate Candidate, files map[string]string, req map[string]any, docker bool) string {
	t.Helper()
	name := fmt.Sprintf("probe-%s-%d", candidate, time.Now().UnixNano())
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	// ConfigMap keys cannot contain '/', so the bootstrap reconstructs files
	// from a JSON bundle before handing stdin to the unmodified library probe.
	bundle, err := json.Marshal(files)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CoreV1().ConfigMaps("kernel").Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name}, Data: map[string]string{"request.json": string(data), "files.json": string(bundle)}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	no, yes := false, true
	zero := int64(0)
	bootstrap := `node -e 'const fs=require("fs"),p=require("path");for(const [n,v] of Object.entries(JSON.parse(fs.readFileSync("/config/files.json")))){const f=p.join("/workspace",n);fs.mkdirSync(p.dirname(f),{recursive:true});fs.writeFileSync(f,v)}' && cd /workspace && git init -q && git config user.email test@example.invalid && git config user.name probe && git add . && git commit -qm fixture && git remote add origin https://github.com/forgelet/probe && `
	if docker {
		bootstrap += `for i in $(seq 1 90); do docker info >/dev/null 2>&1 && break; sleep 1; done; docker info >/dev/null || exit 20; `
	}
	if _, exists := files["github-api.js"]; exists {
		bootstrap += `node /workspace/github-api.js & `
	}
	if req["cache"] == true {
		bootstrap += `for attempt in 1 2; do /usr/local/bin/worker-` + string(candidate) + ` < /config/request.json || exit 21; done`
	} else {
		bootstrap += `exec /usr/local/bin/worker-` + string(candidate) + ` < /config/request.json`
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kernel"}, Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: &no, TerminationGracePeriodSeconds: &zero,
		SecurityContext: &corev1.PodSecurityContext{RunAsUser: &zero},
		Containers:      []corev1.Container{{Name: "worker", Image: image, ImagePullPolicy: corev1.PullNever, Command: []string{"sh", "-c", bootstrap}, Env: []corev1.EnvVar{{Name: "HOME", Value: "/workspace/home"}, {Name: "DOCKER_HOST", Value: "unix:///run/probe/docker.sock"}}, VolumeMounts: []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}, {Name: "cache", MountPath: "/cache"}, {Name: "config", MountPath: "/config", ReadOnly: true}, {Name: "socket", MountPath: "/run/probe"}}}},
		Volumes:         []corev1.Volume{{Name: "workspace", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}, {Name: "cache", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}, {Name: "socket", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}, {Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: name}}}}}}}
	if docker {
		// Native sidecar terminates when worker exits. The engine's socket and
		// entire data directory exist only inside this job's Pod.
		always := corev1.ContainerRestartPolicyAlways
		pod.Spec.InitContainers = []corev1.Container{{Name: "engine", Image: "docker:28.5.1-dind", ImagePullPolicy: corev1.PullNever, RestartPolicy: &always, Command: []string{"dockerd", "--host=unix:///run/probe/docker.sock", "--storage-driver=vfs"}, SecurityContext: &corev1.SecurityContext{Privileged: &yes}, VolumeMounts: []corev1.VolumeMount{{Name: "socket", MountPath: "/run/probe"}, {Name: "workspace", MountPath: "/workspace"}, {Name: "cache", MountPath: "/cache"}, {Name: "engine-data", MountPath: "/var/lib/docker"}}}}
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{Name: "engine-data", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}})
	}
	created, err := client.CoreV1().Pods("kernel").Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		uid := created.UID
		_ = client.CoreV1().Pods("kernel").Delete(cleanup, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
		_ = client.CoreV1().ConfigMaps("kernel").Delete(cleanup, name, metav1.DeleteOptions{})
	})
	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		current, err := client.CoreV1().Pods("kernel").Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if current.Status.Phase == corev1.PodSucceeded || current.Status.Phase == corev1.PodFailed {
			logs, err := client.CoreV1().Pods("kernel").GetLogs(name, &corev1.PodLogOptions{Container: "worker"}).DoRaw(ctx)
			if err != nil {
				t.Fatal(err)
			}
			// Completed jobs must not leave an engine running in the Pod.
			for _, sidecar := range current.Status.InitContainerStatuses {
				if sidecar.State.Running != nil {
					t.Fatal("completed job retained a running engine")
				}
			}
			return string(logs)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Second):
		}
	}
	for _, container := range []string{"worker", "engine"} {
		logs, _ := client.CoreV1().Pods("kernel").GetLogs(name, &corev1.PodLogOptions{Container: container}).DoRaw(ctx)
		t.Logf("%s logs: %s", container, logs)
	}
	t.Fatal("kernel Pod did not finish")
	return ""
}

func kernelFixture(scenario string) (map[string]string, string, string, bool) {
	files := map[string]string{
		"node/action.yml":      "name: node\ndescription: probe\nruns:\n  using: node20\n  main: main.js\n  post: post.js\n",
		"node/main.js":         `require('fs').appendFileSync(process.env.GITHUB_OUTPUT,'answer=42\n');console.log(process.env.TOKEN||'NODE_OK');`,
		"node/post.js":         `console.log('POST_OK');`,
		"composite/action.yml": "name: composite\ndescription: probe\noutputs:\n  answer:\n    value: ${{ steps.node.outputs.answer }}\nruns:\n  using: composite\n  steps:\n    - id: node\n      uses: ./node\n",
	}
	if strings.HasPrefix(scenario, "workflow") {
		files[".github/workflows/callee.yml"] = `name: child
on:
  workflow_call:
    inputs:
      number:
        type: number
        required: true
    outputs:
      answer:
        value: ${{ jobs.only.outputs.answer }}
jobs:
  only:
    runs-on: ubuntu-latest
    outputs:
      answer: ${{ steps.value.outputs.answer }}
    steps:
      - uses: actions/checkout@v4
      - id: value
        env:
          NUMBER: ${{ inputs.number }}
          TOKEN: ${{ secrets.TOKEN }}
        run: |
          test "$NUMBER" = 42
          test -n "$TOKEN"
          echo "answer=$NUMBER" >> "$GITHUB_OUTPUT"
`
		root := `name: parent
on: push
jobs:
  call:
    uses: ./.github/workflows/callee.yml
    with:
      number: 42
    secrets: inherit
  after:
    needs: call
    runs-on: ubuntu-latest
    steps:
      - env:
          ANSWER: ${{ needs.call.outputs.answer }}
        run: test "$ANSWER" = 42 && echo WORKFLOW_OUTPUT_42
`
		if scenario == "workflow-failure" {
			files[".github/workflows/callee.yml"] += "      - run: exit 17\n"
			root = strings.Replace(root, `test "$ANSWER" = 42 && echo WORKFLOW_OUTPUT_42`, "echo FORBIDDEN_EXECUTED", 1)
			root += "  cleanup:\n    needs: call\n    if: always()\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo WORKFLOW_CLEANUP\n"
			return files, root, "failure", false
		}
		return files, root, "success", false
	}
	workflow := "name: lifecycle\non: push\njobs:\n  forbidden:\n    runs-on: ubuntu-latest\n    steps:\n      - run: exit 99\n  only:\n    runs-on: ubuntu-latest\n    outputs:\n      answer: ${{ steps.action.outputs.answer }}\n    steps:\n      - uses: actions/checkout@v4\n      - id: action\n        uses: ./composite\n        env:\n          TOKEN: ${{ secrets.TOKEN }}\n"
	switch scenario {
	case "cache":
		workflow = `name: cache
on: push
jobs:
  only:
    runs-on: ubuntu-latest
    outputs:
      hit: ${{ steps.cache.outputs.cache-hit }}
    steps:
      - uses: actions/checkout@v4
      - id: cache
        uses: actions/cache@v4
        with:
          path: cache-data
          key: kernel-probe-v1
      - run: |
          if test -f cache-data/value; then test "$(cat cache-data/value)" = cached; else mkdir -p cache-data; echo cached > cache-data/value; fi
`
		return files, workflow, "success", false
	case "build-push":
		// Metadata requires a GitHub API token. Exercise the real HTTP client
		// contract with a local read-only API fixture; no personal token or
		// production registry credentials are needed by the public corpus.
		files["github-api.js"] = `require('http').createServer((req,res)=>{if(req.url!='/repos/forgelet/probe'||!['token fixture-github-api-token','Bearer fixture-github-api-token','bearer fixture-github-api-token'].includes(req.headers.authorization)){res.writeHead(401);res.end('{}');return}res.setHeader('Content-Type','application/json');res.end(JSON.stringify({name:'probe',full_name:'forgelet/probe',description:'Kernel integration fixture',html_url:'https://github.com/forgelet/probe',homepage:null,default_branch:'main',owner:{login:'forgelet'},license:{spdx_id:'MIT'}}))}).listen(18081,'127.0.0.1');`
		files["Dockerfile"] = "FROM busybox:1.37.0\nRUN --mount=type=secret,id=probe test -s /run/secrets/probe\nCOPY --from=extra value /value\nCMD [\"cat\",\"/value\"]\n"
		files["extra/value"] = "REGISTRY_IMAGE_VERIFIED\n"
		workflow = `name: build-push
on: push
jobs:
  only:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: docker run -d --name registry -p 5000:5000 registry:2.8.3
      - uses: docker/login-action@v3
        with:
          registry: localhost:5000
          username: probe
          password: probe
      - name: Verify GitHub API contract
        run: |
          if test "$GITHUB_API_URL" != http://127.0.0.1:18081; then echo API_ENDPOINT_MISMATCH; exit 18; fi
      - uses: docker/metadata-action@v5
        id: meta
        with:
          images: localhost:5000/probe
          flavor: latest=false
          tags: type=raw,value=smoke
      - uses: docker/build-push-action@v6
        with:
          context: .
          push: true
          tags: ${{ steps.meta.outputs.tags }}
          secrets: probe=${{ secrets.TOKEN }}
          build-contexts: extra=./extra
      - run: docker pull localhost:5000/probe:smoke && docker run --rm localhost:5000/probe:smoke
`
		return files, workflow, "success", true
	case "failure":
		workflow += "      - run: exit 17\n      - if: always()\n        run: echo CLEANUP_OK\n"
		return files, workflow, "failure", false
	case "cancel":
		workflow = "name: cancel\non: push\njobs:\n  only:\n    runs-on: ubuntu-latest\n    steps:\n      - run: exec sleep 60\n"
		return files, workflow, "cancelled", false
	case "docker":
		files["container/action.yml"] = "name: container\ndescription: probe\nruns:\n  using: docker\n  image: Dockerfile\n"
		entrypoint, _ := json.Marshal([]string{"sh", "-c", "echo DOCKER_ACTION_OK; echo docker=yes >> \"$GITHUB_OUTPUT\""})
		files["container/Dockerfile"] = "FROM busybox:1.37.0\nENTRYPOINT " + string(entrypoint) + "\n"
		workflow = "name: docker\non: push\njobs:\n  only:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@v4\n      - uses: ./container\n"
		return files, workflow, "success", true
	}
	return files, workflow, "success", false
}

// These assertions are also used by the opt-in business probe; they deliberately
// compare the committed files, not the user's uncommitted workspace contents.
func committedFile(ctx context.Context, repo, revision, path string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "show", revision+":"+path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("read committed %s: %w: %s", path, err, stderr.String())
	}
	return data, nil
}
