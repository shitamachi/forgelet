//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	dockercontainer "github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// This opt-in image contains a pinned, CPU-portable DuckDB native library.
// The normal cluster suite does not acquire this optional application dependency.
func TestPortableDuckDBTools(t *testing.T) {
	image := os.Getenv("FORGELET_TEST_DUCKDB_IMAGE")
	if image == "" {
		t.Skip("set FORGELET_TEST_DUCKDB_IMAGE to the built portable-tools image")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Minute)
	defer cancel()
	fixture, err := os.ReadFile(filepath.Join("..", "fixtures", "duckdb", "probe_test.go.txt"))
	must(t, err)
	mod := []byte("module forgelet-duckdb-probe\n\ngo 1.26.4\n\nrequire github.com/duckdb/duckdb-go/v2 v2.20000.0\n")
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: image, ImagePlatform: "linux/amd64", Entrypoint: []string{"sh", "-ec"},
			Cmd: []string{"test \"$GOFLAGS\" = -tags=duckdb_use_lib; /opt/forgelet/duckdb/bin/duckdb-probe; export PATH=/opt/hostedtoolcache/go/1.26.4/x64/bin:$PATH; cd /tmp/probe; go test -mod=mod -race -count=2 -v ."},
			Env: map[string]string{"GOTOOLCHAIN": "local", "GOMAXPROCS": "2"},
			Files: []testcontainers.ContainerFile{
				{Reader: bytes.NewReader(mod), ContainerFilePath: "/tmp/probe/go.mod", FileMode: 0644},
				{Reader: bytes.NewReader(fixture), ContainerFilePath: "/tmp/probe/probe_test.go", FileMode: 0644},
			},
			ConfigModifier:     func(c *dockercontainer.Config) { c.User = "0:0" },
			HostConfigModifier: func(h *dockercontainer.HostConfig) { h.NanoCPUs = 2e9; h.Memory = 2 << 30 },
			WaitingFor:         wait.ForExit().WithExitTimeout(19 * time.Minute),
		}, Started: true,
	})
	if c != nil {
		testcontainers.CleanupContainer(t, c)
		logs, logErr := c.Logs(ctx)
		if logErr == nil {
			b, readErr := io.ReadAll(logs)
			_ = logs.Close()
			t.Log(string(b))
			must(t, readErr)
		}
	}
	must(t, err)
	state, err := c.State(ctx)
	must(t, err)
	if state.ExitCode != 0 {
		t.Fatalf("portable DuckDB probe exit=%d", state.ExitCode)
	}
}
