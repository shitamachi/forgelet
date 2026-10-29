//go:build integration

package integration_test

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dockercontainer "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/testcontainers/testcontainers-go"
)

// Verify all real public archives in the same supervisor image deployed to the
// homelab. The probe binary is a read-only local test mount, not shipped in the
// production image. No host Docker socket or Kubernetes credentials are mounted.
func TestPreloadedActionImageArchives(t *testing.T) {
	image := os.Getenv("FORGELET_TEST_ACTION_BUNDLE_IMAGE")
	if image == "" {
		t.Skip("set FORGELET_TEST_ACTION_BUNDLE_IMAGE for the optional image bundle regression")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "bundle-probe.test")
	build := exec.CommandContext(ctx, "go", "test", "-c", "-o", binary, "./internal/action")
	build.Dir = filepath.Join("..", "..")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build actual archive probe: %v: %s", err, output)
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{
		Image: image, ImagePlatform: "linux/amd64", Entrypoint: []string{"/bundle-probe.test"}, Cmd: []string{"-test.run=^TestPreloadedImageArchives$", "-test.v", "-test.timeout=3m"},
		Env: map[string]string{"FORGELET_TEST_IMAGE_ACTION_ROOT": "/opt/forgelet/action-sources"},
		HostConfigModifier: func(h *dockercontainer.HostConfig) {
			h.ReadonlyRootfs = true
			h.NetworkMode = "none"
			h.Memory = 512 << 20
			h.NanoCPUs = 1e9
			h.Tmpfs = map[string]string{"/tmp": "rw,nosuid,nodev,size=192m,mode=1777"}
			h.Mounts = append(h.Mounts, mount.Mount{Type: mount.TypeBind, Source: binary, Target: "/bundle-probe.test", ReadOnly: true})
		},
	}, Started: true})
	if c != nil {
		testcontainers.CleanupContainer(t, c)
	}
	must(t, err)
	for {
		state, err := c.State(ctx)
		must(t, err)
		if !state.Running {
			logs, err := c.Logs(ctx)
			must(t, err)
			defer func() { must(t, logs.Close()) }()
			output, err := io.ReadAll(io.LimitReader(logs, 64<<10))
			must(t, err)
			t.Log(string(output))
			if !strings.Contains(string(output), "--- PASS: TestPreloadedImageArchives") {
				t.Fatal("missing actual archive probe result")
			}
			if state.ExitCode != 0 {
				t.Fatalf("archive image probe exit=%d", state.ExitCode)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}
