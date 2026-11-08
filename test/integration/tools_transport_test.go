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
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Runner image environment is inherited by user programs. A download workaround
// must not silently disable HTTP/2 in every repository's application and tests.
func TestToolsImagePreservesHTTP2(t *testing.T) {
	image := os.Getenv("FORGELET_TEST_TOOLS_IMAGE")
	if image == "" {
		t.Skip("set FORGELET_TEST_TOOLS_IMAGE for the deployed tools image regression")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	dir := t.TempDir()
	source := filepath.Join(dir, "probe.go")
	must(t, os.WriteFile(source, []byte(`package main
import ("fmt"; "net/http"; "net/http/httptest"; "os")
func main() {
 s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
 s.EnableHTTP2 = true
 s.StartTLS()
 defer s.Close()
 tr := s.Client().Transport.(*http.Transport).Clone()
 tr.DisableKeepAlives = true
 tr.ForceAttemptHTTP2 = true
 defer tr.CloseIdleConnections()
 r, err := (&http.Client{Transport: tr}).Get(s.URL)
 if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
 defer r.Body.Close()
 if r.ProtoMajor != 2 { fmt.Fprintln(os.Stderr, "image disabled HTTP/2:", r.Proto); os.Exit(1) }
 fmt.Println("HTTP/2 preserved")
}
`), 0600))
	binary := filepath.Join(dir, "transport-probe")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, source)
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build transport probe: %v: %s", err, output)
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: image, ImagePlatform: "linux/amd64", Entrypoint: []string{"/transport-probe"},
			Files:      []testcontainers.ContainerFile{{HostFilePath: binary, ContainerFilePath: "/transport-probe", FileMode: 0555}},
			WaitingFor: wait.ForExit().WithExitTimeout(time.Minute),
			HostConfigModifier: func(h *dockercontainer.HostConfig) {
				h.NetworkMode = "none"
				h.Memory = 128 << 20
				h.NanoCPUs = 1e9
			},
		}, Started: true,
	})
	if c != nil {
		testcontainers.CleanupContainer(t, c)
	}
	must(t, err)
	// Files are copied through the Docker API: the daemon need not see the
	// caller's temporary directory, including when it is Pod-owned or remote.
	logs, err := c.Logs(ctx)
	must(t, err)
	defer func() { must(t, logs.Close()) }()
	output, err := io.ReadAll(io.LimitReader(logs, 4096))
	must(t, err)
	t.Log(string(output))
	state, err := c.State(ctx)
	must(t, err)
	if state.ExitCode != 0 || !strings.Contains(string(output), "HTTP/2 preserved") {
		t.Fatalf("tools transport probe exit=%d output=%q", state.ExitCode, output)
	}
}
