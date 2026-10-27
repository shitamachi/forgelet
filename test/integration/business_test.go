//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	dockerclient "github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"

	"github.com/shitamachi/forgelet/internal/run"
	"github.com/shitamachi/forgelet/internal/server"
)

// The opt-in source directory contains clean gh clones, never account credentials.
// A disposable runner serves their exact Git objects through file:// remotes.
// The original checkout Action and every original workflow byte are retained.
func testBusinessWorkflows(t *testing.T, ctx context.Context, cluster testcontainers.Container, client *kubernetes.Clientset, httpClient *http.Client, endpoint, image, root string, request func(*testing.T, string, string, any, int) []byte, get func(*testing.T, string) run.Run) {
	testOriginalWorkflows(t, ctx, cluster, client, httpClient, endpoint, image, root, request, get, "original Go and Node business workflows with isolated image publication", "FORGELET_TEST_BUSINESS_DIR", "steam-business", []string{"steam-platform-server/check.yml", "steam-admin-web/ci.yml", "steam-platform-server/check.yml", "steam-admin-web/ci.yml", "steam-admin-web/image.yml"}, false)
}
func testBuildBusinessWorkflows(t *testing.T, ctx context.Context, cluster testcontainers.Container, client *kubernetes.Clientset, httpClient *http.Client, endpoint, image, root string, request func(*testing.T, string, string, any, int) []byte, get func(*testing.T, string) run.Run) {
	testOriginalWorkflows(t, ctx, cluster, client, httpClient, endpoint, image, root, request, get, "original Buildx production workflows and dual registry publication", "FORGELET_TEST_BUILD_BUSINESS_DIR", "steam-build", []string{"steam-admin/image.yml", "steam-platform-web/check.yml", "steam-platform-web/check.yml", "steam-login/docker.yml"}, true)
}
func testOriginalWorkflows(t *testing.T, ctx context.Context, cluster testcontainers.Container, client *kubernetes.Clientset, httpClient *http.Client, endpoint, image, root string, request func(*testing.T, string, string, any, int) []byte, get func(*testing.T, string) run.Run, suite, sourceEnv, fixture string, runs []string, build bool) {
	t.Run(suite, func(t *testing.T) {
		previousTimeout := httpClient.Timeout
		httpClient.Timeout = 2 * time.Minute
		defer func() { httpClient.Timeout = previousTimeout }()
		snapshots := os.Getenv(sourceEnv)
		if snapshots == "" {
			t.Skip("set " + sourceEnv + " to verified clean business clones; no user GitHub credentials are read")
		}
		// The earlier disk-pressure scenario retains its 256 MiB filesystem.
		// Original Go/npm caches need a larger, still bounded test-only volume.
		clusterCommand(t, ctx, cluster, "mount", "-o", "remount,size=2g", testStorageRoot)
		ensureTestRegistry(t, ctx, client)
		domainIP := businessRegistryProxy(t, ctx, client, image)
		apiService, err := client.CoreV1().Services("forgelet").Get(ctx, "forgelet", metav1.GetOptions{})
		must(t, err)
		if apiService.Spec.Ports[0].Name == "" {
			apiService.Spec.Ports[0].Name = "https"
		}
		if len(apiService.Spec.Ports) == 1 {
			apiService.Spec.Ports = append(apiService.Spec.Ports, corev1.ServicePort{Name: "github-api", Port: 9443, TargetPort: intstr.FromInt32(9443)})
		}
		_, err = client.CoreV1().Services("forgelet").Update(ctx, apiService, metav1.UpdateOptions{})
		must(t, err)

		// The Docker CLI/buildx auth client runs inside the job as well as in the
		// engine. Route the hostname through this disposable cluster's DNS too.
		dns, err := client.CoreV1().ConfigMaps("kube-system").Get(ctx, "coredns", metav1.GetOptions{})
		must(t, err)
		oldCorefile := dns.Data["Corefile"]
		dns.Data["Corefile"] = "registry.shitamchi.com ghcr.io:53 {\n  errors\n  hosts {\n    " + domainIP + " registry.shitamchi.com ghcr.io\n  }\n}\n" + oldCorefile
		_, err = client.CoreV1().ConfigMaps("kube-system").Update(ctx, dns, metav1.UpdateOptions{})
		must(t, err)
		clusterCommand(t, ctx, cluster, "kubectl", "rollout", "restart", "deployment/coredns", "-n", "kube-system")
		clusterCommand(t, ctx, cluster, "kubectl", "rollout", "status", "deployment/coredns", "-n", "kube-system", "--timeout=90s")
		defer func() {
			dns, err := client.CoreV1().ConfigMaps("kube-system").Get(ctx, "coredns", metav1.GetOptions{})
			must(t, err)
			dns.Data["Corefile"] = oldCorefile
			_, err = client.CoreV1().ConfigMaps("kube-system").Update(ctx, dns, metav1.UpdateOptions{})
			must(t, err)
		}()

		manifest, err := os.ReadFile(filepath.Join(root, "test/fixtures", fixture, "sources.json"))
		must(t, err)
		var sources map[string]struct {
			Repository      string `json:"repository"`
			Commit          string `json:"commit"`
			Path            string `json:"path"`
			ExecutionCommit string `json:"-"`
		}
		must(t, json.Unmarshal(manifest, &sources))
		stage := t.TempDir()
		must(t, os.Mkdir(filepath.Join(stage, "repos"), 0700))
		var dockerfile strings.Builder
		publicCA, err := client.CoreV1().Secrets("forgelet").Get(ctx, "forgelet-api-tls", metav1.GetOptions{})
		must(t, err)
		must(t, os.WriteFile(filepath.Join(stage, "ca.crt"), publicCA.Data["tls.crt"], 0600))
		fmt.Fprintf(&dockerfile, "FROM %s\nUSER root\n", image)
		if cached := os.Getenv("FORGELET_TEST_TOOL_CACHE_IMAGE"); build && cached != "" {
			if strings.ContainsAny(cached, " \t\r\n") {
				t.Fatal("invalid explicit browser cache image")
			}
			fmt.Fprintf(&dockerfile, "COPY --from=%s /opt/forgelet/test-browsers /opt/forgelet/test-browsers\nCOPY --from=%s /opt/hostedtoolcache /opt/hostedtoolcache\nENV PLAYWRIGHT_BROWSERS_PATH=/opt/forgelet/test-browsers\n", cached, cached)
			t.Log("explicit execution image provides official Node24 and Playwright browsers; setup Actions and original install/tests remain active")
		}
		dockerfile.WriteString("COPY repos /opt/forgelet/business\nCOPY ca.crt /opt/forgelet/business-ca.crt\nENV NODE_EXTRA_CA_CERTS=/opt/forgelet/business-ca.crt\n")
		seen := map[string]bool{}
		for name, source := range sources {
			repo := strings.Split(name, "/")[0]
			if build && repo == "steam-api" {
				continue
			}
			if seen[repo] {
				continue
			}
			seen[repo] = true
			src := filepath.Join(snapshots, repo)
			sha := strings.TrimSpace(businessCommand(t, ctx, "git", "-C", src, "rev-parse", "HEAD"))
			if sha != source.Commit || strings.TrimSpace(businessCommand(t, ctx, "git", "-C", src, "status", "--porcelain")) != "" {
				t.Fatal("business snapshot is not clean/fixed", repo, sha)
			}
			if build && repo == "steam-platform-web" && os.Getenv("FORGELET_TEST_PLATFORM_WEB_FIX") == "1" {
				var executionCommit string
				src, executionCommit = preparePlatformWebFixture(t, ctx, root, src, source.Commit)
				for key, entry := range sources {
					if strings.HasPrefix(key, repo+"/") {
						entry.ExecutionCommit = executionCommit
						sources[key] = entry
					}
				}
			}
			businessCommand(t, ctx, "git", "clone", "--bare", "--no-hardlinks", src, filepath.Join(stage, "repos", repo+".git"))
			fmt.Fprintf(&dockerfile, "RUN git config --system url.\"file:///opt/forgelet/business/%s.git\".insteadOf https://github.com/%s && git config --system --add url.\"file:///opt/forgelet/business/%s.git\".insteadOf https://github.com/%s.git\n", repo, source.Repository, repo, source.Repository)
		}
		dockerfile.WriteString("RUN chmod -R a+rX /opt/forgelet/business\nUSER 1000:1000\n")
		must(t, os.WriteFile(filepath.Join(stage, "Dockerfile"), []byte(dockerfile.String()), 0600))
		runnerLocal := buildBusinessFixtureImage(t, ctx, stage, "forgelet-business-runner")
		runner := pushTestImage(t, ctx, cluster, client, runnerLocal, "business-runner:test")
		engineDir := t.TempDir()
		engineDockerfile := "FROM docker:28.5.1-dind\nRUN mv /usr/local/bin/dockerd /usr/local/bin/dockerd.real && mkdir -p /etc/docker\nCOPY daemon.json /etc/docker/daemon.json\nCOPY dockerd /usr/local/bin/dockerd\nRUN chmod 755 /usr/local/bin/dockerd\n"
		must(t, os.WriteFile(filepath.Join(engineDir, "Dockerfile"), []byte(engineDockerfile), 0600))
		must(t, os.WriteFile(filepath.Join(engineDir, "daemon.json"), []byte(`{}`), 0600))
		must(t, os.WriteFile(filepath.Join(engineDir, "dockerd"), []byte("#!/bin/sh\nset -eu\necho '"+domainIP+" registry.shitamchi.com ghcr.io' >> /etc/hosts\nexec /usr/local/bin/dockerd.real --insecure-registry=registry.shitamchi.com --insecure-registry=ghcr.io \"$@\"\n"), 0700))
		engineLocal := buildBusinessFixtureImage(t, ctx, engineDir, "forgelet-business-engine")
		engine := pinTestRegistryImage(t, ctx, cluster, pushTestImage(t, ctx, cluster, client, engineLocal, "business-engine:test"))
		clusterCommand(t, ctx, cluster, "ctr", "--namespace=k8s.io", "images", "pull", "--plain-http", "--user", "test:integration-registry-password", engine)
		mirrors := map[string]string{}
		aliases := []string{"node:24-alpine", "nginxinc/nginx-unprivileged:1.27-alpine"}
		if build {
			aliases = append(aliases, "moby/buildkit:buildx-stable-1", "golang:1.27.1-alpine", "alpine:3.21")
		}
		for i, alias := range aliases {
			t.Logf("prepare fixed-version public base image %s", alias)
			sourceImage := alias
			if alias == "moby/buildkit:buildx-stable-1" {
				sourceImage = "moby/buildkit:v0.20.2"
			}
			pullBusinessFixtureImage(t, ctx, sourceImage)
			mirrors[alias] = pinTestRegistryImage(t, ctx, cluster, pushTestImage(t, ctx, cluster, client, sourceImage, fmt.Sprintf("business-base-%d:test", i)))
		}
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
		var profileText strings.Builder
		fmt.Fprintf(&profileText, "profiles:\n  ubuntu-latest:\n    image: %s\n    memory_limit: 2Gi\n    act:\n      runner_image: %s\n      engine_image: %s\n      registry_secret: forgelet-registry\n      insecure_registry: true\n      image_mirrors:\n", image, runner, engine)
		for _, alias := range aliases {
			fmt.Fprintf(&profileText, "        %s: %s\n", alias, mirrors[alias])
		}
		if build {
			profileText.WriteString("      buildkit_config: |\n        [registry.\"docker.io\"]\n          mirrors = [\"docker.m.daocloud.io\"]\n        [registry.\"registry.shitamchi.com\"]\n          insecure = true\n        [registry.\"ghcr.io\"]\n          insecure = true\n")
		}
		profile := profileText.String()
		cm.Data["forgelet.yaml"] = strings.Replace(strings.Replace(previous, "profiles:\n", profile, 1), "github_api_url: https://localhost:9443", "github_api_url: https://forgelet.forgelet.svc:9443", 1)
		if build {
			cm.Data["forgelet.yaml"] += "\npackage_tokens:\n  shitamachi/steam-login: GHCR_TOKEN\n"
		}
		_, err = client.CoreV1().ConfigMaps("forgelet").Update(ctx, cm, metav1.UpdateOptions{})
		must(t, err)
		restartServer(t, ctx, client, httpClient, endpoint)
		var management server.Management
		must(t, json.Unmarshal(request(t, "GET", "/api/management", nil, 200), &management))
		oldManagement := management
		management.Repositories = append([]server.RepositoryConfig(nil), management.Repositories...)
		for repo := range seen {
			management.Repositories = append(management.Repositories, server.RepositoryConfig{Name: "shitamachi/" + repo, Enabled: true, InstallationID: 7})
		}
		request(t, "PUT", "/api/management", management, 200)
		defer func() {
			must(t, json.Unmarshal(request(t, "GET", "/api/management", nil, 200), &management))
			oldManagement.Version = management.Version
			request(t, "PUT", "/api/management", oldManagement, 200)
		}()
		for repo := range seen {
			values := map[string]string{"REGISTRY_USERNAME": "test", "REGISTRY_PASSWORD": "integration-registry-password"}
			if repo == "steam-login" {
				values = map[string]string{"GHCR_TOKEN": "integration-ghcr-package-token", "DOCKER_REGISTRY": "registry.shitamchi.com", "DOCKER_USERNAME": "test", "DOCKER_PASSWORD": "integration-registry-password"}
			}
			for name, value := range values {
				path := "/api/repositories/shitamachi/" + repo + "/secrets/" + name
				request(t, "PUT", path, map[string]string{"value": value}, 200)
				defer request(t, "DELETE", path, nil, 200)
			}
		}
		logs := func(item run.Run) string {
			var out strings.Builder
			for _, job := range item.Jobs {
				var after int64
				for {
					var page []run.Log
					must(t, json.Unmarshal(request(t, "GET", fmt.Sprintf("/api/jobs/%s/logs?after=%d", job.ID, after), nil, 200), &page))
					if len(page) == 0 {
						break
					}
					for _, chunk := range page {
						out.WriteString(chunk.Data)
						after = chunk.Seq
					}
				}
			}
			return out.String()
		}
		attempts := map[string]int{}
		previousImageUID := ""
		for _, name := range runs {
			if selected := os.Getenv("FORGELET_TEST_BUILD_BUSINESS_ONLY"); build && selected != "" && !strings.HasPrefix(name, selected+"/") {
				continue
			}
			if !build && os.Getenv("FORGELET_TEST_BUSINESS_ONLY_IMAGE") == "1" && name != "steam-admin-web/image.yml" {
				continue
			}
			attempts[name]++
			source := sources[name]
			raw, err := os.ReadFile(filepath.Join(root, "test/fixtures", fixture, name))
			must(t, err)
			actual, err := os.ReadFile(filepath.Join(snapshots, strings.Split(name, "/")[0], source.Path))
			must(t, err)
			if string(actual) != string(raw) {
				t.Fatal("workflow snapshot differs from original", name)
			}
			executionCommit := source.Commit
			if source.ExecutionCommit != "" {
				executionCommit = source.ExecutionCommit
			}
			item := createPublicWorkflow(t, ctx, httpClient, endpoint, map[string]any{"repository": source.Repository, "sha": executionCommit, "ref": "refs/heads/main", "workflow_path": source.Path, "workflow": string(raw), "installation_id": 7, "event_name": "push"})
			// A serial needs chain can legitimately consume each job's own
			// timeout. Do not give two legal 20-minute jobs a 25-minute test cap.
			waitBudget := 5 * time.Minute
			for _, job := range item.Jobs {
				waitBudget += time.Duration(job.Plan.TimeoutSeconds) * time.Second
			}
			waitForTimeout(t, ctx, "original business workflow "+name, waitBudget, func() bool { item = get(t, item.ID); return item.State.Terminal() })
			text := logs(item)
			if item.State != run.Succeeded {
				t.Fatalf("original workflow %s failed: %+v\n%s", name, item, text)
			}
			for _, job := range item.Jobs {
				if len(job.Plan.ActionPins) == 0 || job.Result == nil || len(job.Result.Steps) != len(job.Plan.Steps) {
					t.Fatal("missing fixed sources or original steps", job)
				}
				for _, step := range job.Result.Steps {
					if step.State != run.Succeeded {
						t.Fatal("original step was skipped/failed", name, step)
					}
				}
			}
			if strings.HasSuffix(name, "/ci.yml") || name == "steam-platform-server/check.yml" || name == "steam-platform-web/check.yml" {
				foundCache := false
				for _, job := range item.Jobs {
					for i, step := range job.Plan.Steps {
						if step.Action != nil && (step.Action.Repository == "actions/setup-node" || step.Action.Repository == "actions/setup-go") {
							foundCache = true
							want := strconv.FormatBool(attempts[name] > 1)
							if job.Result.Steps[i].Outputs["cache-hit"] != want {
								t.Fatal("original cold/warm cache was not restored", name, job.Result.Steps[i], text)
							}
						}
					}
				}
				if !foundCache {
					t.Fatal("original cache Action was not checked", name)
				}
			}
			if name == "steam-platform-web/check.yml" {
				imageJob := jobByKey(t, item, "image")
				if attempts[name] > 1 {
					if imageJob.PodUID == previousImageUID || !strings.Contains(text, "importing cache manifest from gha") || !regexp.MustCompile(`RUN pnpm install --frozen-lockfile[^\n]*\n\s*#[0-9]+ CACHED`).MatchString(text) {
						t.Fatal("original image did not restore the dependency layer in a new builder", text)
					}
				}
				previousImageUID = imageJob.PodUID
			}

			t.Logf("original workflow %s base=%s executed=%s run=%s passed; Action lock=%v", name, source.Commit, executionCommit, item.ID, item.Jobs[0].Plan.ActionPins)
			if strings.Contains(text, "integration-registry-password") || strings.Contains(text, "integration-installation-token") {
				t.Fatal("credential leaked in business logs")
			}
			if name == "steam-admin-web/image.yml" || name == "steam-admin/image.yml" || name == "steam-platform-web/check.yml" || name == "steam-login/docker.yml" {
				repo := strings.Split(name, "/")[0]
				tag := "registry.shitamchi.com/" + repo + ":latest"
				if repo == "steam-login" {
					tag = "registry.shitamchi.com/shitamachi/steam-login:latest"
				}
				digest := pinTestRegistryImage(t, ctx, cluster, tag)
				found := false
				for _, job := range item.Jobs {
					for _, step := range job.Result.Steps {
						if strings.HasSuffix(digest, "@"+step.Outputs["digest"]) && step.Outputs["digest"] != "" {
							found = true
						}
					}
				}
				if !found {
					t.Fatal("published manifest differs from original build Action digest", digest, item.Jobs[0].Result)
				}
				t.Logf("isolated original image publication: %s", digest)
				if repo == "steam-platform-web" {
					versioned := pinTestRegistryImage(t, ctx, cluster, "registry.shitamchi.com/steam-platform-web:sha-"+executionCommit)
					if strings.SplitN(versioned, "@", 2)[1] != strings.SplitN(digest, "@", 2)[1] {
						t.Fatal("SHA and latest tags disagree", versioned, digest)
					}
					t.Logf("isolated original source-SHA tag: %s", versioned)
				}

				if repo == "steam-login" {
					ghcr := pinTestRegistryImage(t, ctx, cluster, "registry.shitamchi.com/ghcr-fixture/shitamachi/steam-login:latest")
					t.Logf("isolated original GHCR publication: %s", ghcr)
				}
			}
		}
	})
}
func businessCommand(t *testing.T, ctx context.Context, name string, args ...string) string {
	t.Helper()
	data, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("business fixture %s: %v: %s", name, err, data)
	}
	return string(data)
}
func buildBusinessFixtureImage(t *testing.T, ctx context.Context, dir, repo string) string {
	t.Helper()
	tag := fmt.Sprintf("test-%d", time.Now().UnixNano())
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{FromDockerfile: testcontainers.FromDockerfile{Context: dir, Repo: repo, Tag: tag, BuildLogWriter: os.Stdout}}, Started: false})
	if container != nil {
		testcontainers.CleanupContainer(t, container)
	}
	must(t, err)
	return repo + ":" + tag
}
func pullBusinessFixtureImage(t *testing.T, ctx context.Context, image string) {
	t.Helper()
	docker, err := testcontainers.NewDockerClientWithOpts(ctx)
	must(t, err)
	defer func() { _ = docker.Close() }()
	if _, err := docker.ImageInspect(ctx, image); err == nil {
		return
	}
	pull, err := docker.ImagePull(ctx, image, dockerclient.ImagePullOptions{})
	must(t, err)
	defer func() { _ = pull.Close() }()
	must(t, pull.Wait(ctx))
}
