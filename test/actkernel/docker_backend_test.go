//go:build integration

package actkernel

import (
	"context"
	"net/netip"
	"os"
	"os/exec"
	"testing"
	"time"

	dockercontainer "github.com/moby/moby/api/types/container"
	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// TestDockerBackendRace runs the native race-instrumented Go kernel against a
// Testcontainers-owned engine. Jobs use Docker Exec, not the stock host PTY.
func TestDockerBackendRace(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	engine, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{
		Image: "docker:28.5.1-dind", Env: map[string]string{"DOCKER_TLS_CERTDIR": ""},
		Cmd:          []string{"dockerd", "--host=tcp://0.0.0.0:2375", "--tls=false", "--storage-driver=vfs"},
		ExposedPorts: []string{"2375/tcp"},
		HostConfigModifier: func(hc *dockercontainer.HostConfig) {
			hc.Privileged = true
			hc.PortBindings = dockernetwork.PortMap{dockernetwork.MustParsePort("2375/tcp"): {{HostIP: netip.MustParseAddr("127.0.0.1")}}}
		},
		WaitingFor: wait.ForHTTP("/_ping").WithPort("2375/tcp").WithStartupTimeout(time.Minute),
	}, Started: true})
	if engine != nil {
		testcontainers.CleanupContainer(t, engine)
	}
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := engine.PortEndpoint(ctx, "2375/tcp", "tcp")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := Prepare(dir, Upstream); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "go", "test", "-mod=readonly", "-race", "-count=1", "-run", "^TestDockerModeOutputs$", "-v", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "DOCKER_HOST="+endpoint, "DOCKER_TLS_VERIFY=", "FORGELET_KERNEL_DOCKER_TEST_IMAGE=node:20.20.0-bookworm-slim")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Docker-backed kernel race regression: %v\n%s", err, output)
	}
	t.Logf("%s", output)
}
