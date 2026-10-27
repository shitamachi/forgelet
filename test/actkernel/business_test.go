//go:build integration

package actkernel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const steamRevision = "8265ad9772c5c92d3724a0372f17c31a4a8e498d"

// TestSteamTasksOriginal is explicit opt-in because it reads private source and
// credentials and executes the original project's real commands. It uses its
// own Linux container and never modifies the source checkout or GitHub.
func TestSteamTasksOriginal(t *testing.T) {
	repo := os.Getenv("FORGELET_KERNEL_STEAM_REPO")
	if repo == "" {
		t.Skip("set FORGELET_KERNEL_STEAM_REPO and FORGELET_KERNEL_SECRETS for the private business probe")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Minute)
	defer cancel()
	secretFile := os.Getenv("FORGELET_KERNEL_SECRETS")
	info, err := os.Stat(secretFile)
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatal("FORGELET_KERNEL_SECRETS must be a private JSON file (mode 0600)")
	}
	secretData, err := os.ReadFile(secretFile)
	if err != nil {
		t.Fatal(err)
	}
	var secrets map[string]string
	if err := json.Unmarshal(secretData, &secrets); err != nil || secrets["GITHUB_TOKEN"] == "" {
		t.Fatal("private probe requires GITHUB_TOKEN")
	}
	root := t.TempDir()
	for _, candidate := range []Candidate{Upstream, Gitea} {
		dir := filepath.Join(root, string(candidate))
		if err := Prepare(dir, candidate); err != nil {
			t.Fatal(err)
		}
		if err := Build(ctx, dir, filepath.Join(root, "worker-"+string(candidate)), "linux", "amd64"); err != nil {
			t.Fatal(err)
		}
	}
	// git archive excludes all uncommitted and untracked user work.
	archive := exec.CommandContext(ctx, "git", "-C", repo, "archive", "--format=tar", steamRevision)
	source, err := archive.Output()
	if err != nil {
		t.Fatal("archive fixed steam-tasks commit: ", err)
	}
	if err := os.WriteFile(filepath.Join(root, "source.tar"), source, 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureRoot, err := filepath.Abs("../fixtures/steam-tasks")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"ci.yml", "image.yml", "reliability.yml"} {
		actual, err := committedFile(ctx, repo, steamRevision, ".github/workflows/"+file)
		if err != nil {
			t.Fatal(err)
		}
		expected, err := os.ReadFile(filepath.Join(fixtureRoot, file))
		if err != nil || !bytes.Equal(actual, expected) {
			t.Fatal("business workflow differs from recorded original: ", file)
		}
	}
	base := os.Getenv("FORGELET_KERNEL_BUSINESS_IMAGE")
	if base == "" {
		base = "registry.shitamchi.com/forgelet/executor:20261025-4f626da"
	}
	dockerfile := `FROM ` + base + `
USER 0:0
RUN apt-get update && apt-get install -y --no-install-recommends curl zstd gcc g++ make && rm -rf /var/lib/apt/lists/*
COPY /worker-upstream /worker-gitea /usr/local/bin/
COPY /source.tar /opt/probe/source.tar
RUN mkdir -p /workspace && chown 0:0 /workspace && cd /workspace && tar xf /opt/probe/source.tar && git init -q -b main && git config user.email probe@example.invalid && git config user.name probe && git add . && git commit -qm snapshot && git remote add origin https://github.com/shitamachi/steam-tasks
WORKDIR /workspace
ENTRYPOINT ["sh", "-c"]
`
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte(dockerfile), 0o600); err != nil {
		t.Fatal(err)
	}
	tag := fmt.Sprintf("business-%d", time.Now().UnixNano())
	build, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{ImagePlatform: "linux/amd64", FromDockerfile: testcontainers.FromDockerfile{Context: root, Repo: "forgelet-kernel", Tag: tag, BuildLogWriter: os.Stdout}}})
	if build != nil {
		testcontainers.CleanupContainer(t, build)
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []Candidate{Upstream, Gitea} {
		t.Run(string(candidate), func(t *testing.T) {
			event := fmt.Sprintf(`{"ref":"refs/heads/main","after":%q,"repository":{"full_name":"shitamachi/steam-tasks","name":"steam-tasks","default_branch":"main","owner":{"login":"shitamachi","name":"shitamachi"}}}`, steamRevision)
			req := map[string]any{"mode": "workflow", "workdir": "/workspace", "workflow": ".github/workflows/ci.yml", "event_name": "push", "event_path": "/opt/probe/event.json", "cache_dir": "/cache", "cache": true, "timeout_seconds": 1200, "secrets": secrets, "env": map[string]string{"GOPROXY": "https://goproxy.cn,direct", "GOMAXPROCS": "2", "GOFLAGS": "-p=2"}}
			requestData, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: "forgelet-kernel:" + tag, ImagePlatform: "linux/amd64", Cmd: []string{"exec /usr/local/bin/worker-" + string(candidate) + " < /opt/probe/request.json"}, Env: map[string]string{"HOME": "/workspace/home", "DOCKER_HOST": "unix:///nonexistent/no-host-socket.sock"}, Files: []testcontainers.ContainerFile{{Reader: bytes.NewReader(requestData), ContainerFilePath: "/opt/probe/request.json", FileMode: 0o600}, {Reader: strings.NewReader(event), ContainerFilePath: "/opt/probe/event.json", FileMode: 0o600}}, WaitingFor: wait.ForExit().WithExitTimeout(22 * time.Minute)}, Started: true})
			if container != nil {
				testcontainers.CleanupContainer(t, container)
			}
			if err != nil {
				t.Fatal(err)
			}
			reader, err := container.Logs(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = reader.Close() }()
			logs, err := io.ReadAll(io.LimitReader(reader, 16<<20))
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range secrets {
				if secret != "" && bytes.Contains(logs, []byte(secret)) {
					t.Fatal("secret escaped the worker log boundary")
				}
			}
			if destination := os.Getenv("FORGELET_KERNEL_EVIDENCE"); destination != "" {
				if err := os.MkdirAll(destination, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(destination, string(candidate)+"-steam-ci.log"), logs, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if !bytes.Contains(logs, []byte(`"state":"success"`)) {
				if len(logs) > 8000 {
					logs = logs[len(logs)-8000:]
				}
				t.Fatalf("original steam-tasks CI did not pass:\n%s", logs)
			}
		})
	}
}
