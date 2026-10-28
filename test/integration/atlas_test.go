//go:build integration

package integration_test

import (
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

// Publication is verified against real Zot. This fixture independently runs the
// business installer and migration checks in Linux without a Docker socket.
func TestMirroredAtlasInstaller(t *testing.T) {
	root := os.Getenv("FORGELET_TEST_ATLAS_SOURCE_DIR")
	image := os.Getenv("FORGELET_TEST_ATLAS_IMAGE")
	if root == "" || image == "" {
		t.Skip("set FORGELET_TEST_ATLAS_SOURCE_DIR and FORGELET_TEST_ATLAS_IMAGE for the Atlas tool fixture")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()
	var files []testcontainers.ContainerFile
	for _, relative := range []string{"scripts/ci", "migrations/steam_games", "migrations/steam_steamdb"} {
		err := filepath.WalkDir(filepath.Join(root, relative), func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			name, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			files = append(files, testcontainers.ContainerFile{HostFilePath: path, ContainerFilePath: "/tmp/business/" + filepath.ToSlash(name), FileMode: 0644})
			return nil
		})
		must(t, err)
	}
	command := `cd /tmp/business
export XDG_CACHE_HOME=/tmp/tool-cache
sh scripts/ci/install-atlas.sh /tmp/atlas
/tmp/atlas version | grep 'v1.3.0'
/tmp/atlas migrate validate --dir file://migrations/steam_games
/tmp/atlas migrate validate --dir file://migrations/steam_steamdb
# A verified cache must work when curl has no working network route.
env HTTPS_PROXY=http://127.0.0.1:1 https_proxy=http://127.0.0.1:1 NO_PROXY= no_proxy= sh scripts/ci/install-atlas.sh /tmp/atlas-warm
cmp /tmp/atlas /tmp/atlas-warm
printf corrupt > /tmp/tool-cache/steam-tasks/atlas/v1.3.0-linux-amd64/atlas
sh scripts/ci/install-atlas.sh /tmp/atlas-repaired
cmp /tmp/atlas /tmp/atlas-repaired
printf '\n-- intentional checksum failure\n' >> "$(find migrations/steam_games -name '*.sql' | head -1)"
if /tmp/atlas migrate validate --dir file://migrations/steam_games; then
  echo 'Corrupt migration unexpectedly accepted'; exit 1
fi
test -z "$(find /tmp/tool-cache -name '*.download.*')"
echo 'Atlas cold download, offline warm cache, corrupt cache repair and real migration validation passed'
`
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: image, ImagePlatform: "linux/amd64", Entrypoint: []string{"sh", "-ec"},
			Cmd: []string{command}, Files: files,
			ConfigModifier: func(c *dockercontainer.Config) { c.User = "0:0" },
			HostConfigModifier: func(h *dockercontainer.HostConfig) {
				h.NanoCPUs = 1e9
				h.Memory = 512 << 20
			},
			WaitingFor: wait.ForExit().WithExitTimeout(7 * time.Minute),
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
		t.Fatalf("mirrored Atlas fixture exit=%d", state.ExitCode)
	}
}
