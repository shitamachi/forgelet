//go:build integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/shitamachi/forgelet/internal/run"
)

type toolchainPreload struct{ goFile, nodeFile, arch string }

func loadToolchainPreload(t *testing.T, ctx context.Context, client *kubernetes.Clientset, root string) *toolchainPreload {
	t.Helper()
	dir := os.Getenv("FORGELET_TEST_TOOLCHAINS_DIR")
	if dir == "" {
		return nil
	}
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	must(t, err)
	if len(nodes.Items) != 1 {
		t.Fatal("toolchain preload requires the disposable single-node cluster")
	}
	arch := nodes.Items[0].Status.NodeInfo.Architecture
	nodeArch := arch
	if arch == "amd64" {
		nodeArch = "x64"
	} else if arch != "arm64" {
		t.Fatalf("unsupported toolchain architecture %q", arch)
	}
	data, err := os.ReadFile(filepath.Join(root, "test/fixtures/toolchains.sha256"))
	must(t, err)
	approved := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 64 {
			t.Fatal("invalid toolchain checksum manifest")
		}
		approved[fields[1]] = fields[0]
	}
	files := []string{"go1.25.0.linux-" + arch + ".tar.gz", "node-v24.14.0-linux-" + nodeArch + ".tar.xz"}
	for _, name := range files {
		if approved[name] == "" {
			t.Fatal("unapproved toolchain archive", name)
		}
		must(t, verifyToolchainArchive(filepath.Join(dir, name), approved[name]))
	}
	t.Log("preloading checksum-verified official SDK archives; real setup Actions and binaries still run; external SDK download is not counted")
	return &toolchainPreload{goFile: filepath.Join(dir, files[0]), nodeFile: filepath.Join(dir, files[1]), arch: nodeArch}
}

func verifyToolchainArchive(path, expected string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	stat, err := file.Stat()
	if err != nil {
		return err
	}
	if stat.Size() <= 0 || stat.Size() > 256<<20 {
		return fmt.Errorf("invalid toolchain archive size: %s", path)
	}
	sum := sha256.New()
	if _, err = io.Copy(sum, file); err != nil {
		return err
	}
	if hex.EncodeToString(sum.Sum(nil)) != expected {
		return fmt.Errorf("toolchain archive checksum mismatch: %s", path)
	}
	return nil
}

func (p *toolchainPreload) workflow(source string) string {
	if p == nil {
		return source
	}
	step := fmt.Sprintf(`    steps:
      - name: Prepare verified test toolchains
        run: |
          touch /workspace/repository/toolchains-ready
          while [ ! -f /workspace/repository/toolchains-staged ]; do sleep 1; done
          mkdir -p "$RUNNER_TOOL_CACHE/go/1.25.0/%[1]s" "$RUNNER_TOOL_CACHE/node/24.14.0/%[1]s"
          tar -xzf /workspace/repository/go-sdk.tar.gz --strip-components=1 -C "$RUNNER_TOOL_CACHE/go/1.25.0/%[1]s"
          tar -xJf /workspace/repository/node-sdk.tar.xz --strip-components=1 -C "$RUNNER_TOOL_CACHE/node/24.14.0/%[1]s"
          touch "$RUNNER_TOOL_CACHE/go/1.25.0/%[1]s.complete" "$RUNNER_TOOL_CACHE/node/24.14.0/%[1]s.complete"
`, p.arch)
	return strings.Replace(source, "    steps:\n", step, 1)
}

func (p *toolchainPreload) stage(t *testing.T, ctx context.Context, cluster testcontainers.Container, item run.Run, get func(*testing.T, string) run.Run) {
	t.Helper()
	if p == nil {
		return
	}
	var job run.Job
	waitForTimeout(t, ctx, "toolchain preload checkpoint", 3*time.Minute, func() bool {
		current := get(t, item.ID)
		job = current.Jobs[0]
		if current.State.Terminal() {
			t.Fatal("toolchain preload stopped before staging", current)
		}
		if job.State != run.Running {
			return false
		}
		code, _, err := cluster.Exec(ctx, []string{"kubectl", "exec", "-n", "forgelet", job.PodName, "-c", "executor", "--", "test", "-f", "/workspace/repository/toolchains-ready"})
		return err == nil && code == 0
	})
	for _, file := range []struct{ source, name string }{{p.goFile, "go-sdk.tar.gz"}, {p.nodeFile, "node-sdk.tar.xz"}} {
		temporary := "/tmp/forgelet-test-" + file.name
		must(t, cluster.CopyFileToContainer(ctx, file.source, temporary, 0o600))
		clusterCommand(t, ctx, cluster, "kubectl", "cp", temporary, "forgelet/"+job.PodName+":/workspace/repository/"+file.name, "-c", "executor")
	}
	execInPod(t, ctx, cluster, job.PodName, "touch /workspace/repository/toolchains-staged")
}

func TestToolchainArchiveRejectsUnverifiedBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sdk.tar.gz")
	must(t, os.WriteFile(path, []byte("verified"), 0o600))
	sum := sha256.Sum256([]byte("verified"))
	approved := hex.EncodeToString(sum[:])
	must(t, verifyToolchainArchive(path, approved))
	must(t, os.WriteFile(path, []byte("tampered"), 0o600))
	if verifyToolchainArchive(path, approved) == nil {
		t.Fatal("accepted changed toolchain bytes")
	}
	if verifyToolchainArchive(path+"-missing", approved) == nil {
		t.Fatal("accepted missing toolchain")
	}
}
