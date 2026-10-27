//go:build integration

package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dockercontainer "github.com/moby/moby/api/types/container"
	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestPreloadedImageWithRealDocker(t *testing.T) {
	ctx, done := context.WithTimeout(t.Context(), 4*time.Minute)
	defer done()
	engine, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{
		Image: "docker:28.5.1-dind", Env: map[string]string{"DOCKER_TLS_CERTDIR": ""},
		Cmd: []string{"dockerd", "--host=tcp://0.0.0.0:2375", "--tls=false", "--storage-driver=overlay2"}, ExposedPorts: []string{"2375/tcp"},
		HostConfigModifier: func(c *dockercontainer.HostConfig) {
			c.Privileged = true
			c.PortBindings = dockernetwork.PortMap{dockernetwork.MustParsePort("2375/tcp"): {{HostIP: netip.MustParseAddr("127.0.0.1")}}}
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
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	configuration, _ := json.Marshal(map[string]any{"cliPluginsExtraDirs": []string{filepath.Join(home, ".docker", "cli-plugins")}})
	if err := os.WriteFile(filepath.Join(dir, "config.json"), configuration, 0600); err != nil {
		t.Fatal(err)
	}
	source := "rancher/mirrored-library-busybox:1.37.0"
	inspect := exec.CommandContext(ctx, "docker", "image", "inspect", "--format={{.Id}}", source)
	id, err := inspect.Output()
	if err != nil {
		t.Fatal("fixed cached fixture image is required", err)
	}
	save := exec.CommandContext(ctx, "docker", "image", "save", "--output", filepath.Join(dir, "fixture.tar"), source)
	if output, err := save.CombinedOutput(); err != nil {
		t.Fatalf("save fixture: %v %s", err, output)
	}
	file, err := os.Open(filepath.Join(dir, "fixture.tar"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	load := exec.CommandContext(ctx, "docker", "image", "load")
	load.Env = append(os.Environ(), "DOCKER_HOST="+endpoint, "DOCKER_TLS_VERIFY=", "DOCKER_CERT_PATH=")
	load.Stdin = file
	if output, err := load.CombinedOutput(); err != nil {
		t.Fatalf("load fixture: %v %s", err, output)
	}
	config := strings.TrimSpace(string(id))
	manifest := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":%q,"size":1},"layers":[]}`, config))
	hash := sha256.Sum256(manifest)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		_, _ = w.Write(manifest)
	}))
	defer server.Close()
	t.Setenv("DOCKER_HOST", endpoint)
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	reference := strings.TrimPrefix(server.URL, "http://") + "/runner@sha256:" + hex.EncodeToString(hash[:])
	got, ok := preloadedActImage(ctx, Options{ActImage: reference, RegistryConfig: dir})
	if !ok || got != config {
		for _, args := range [][]string{{"buildx", "imagetools", "inspect", "--raw", reference}, {"image", "inspect", "--format={{.Id}}", config}} {
			command := exec.CommandContext(ctx, "docker", args...)
			command.Env = append(os.Environ(), "DOCKER_CONFIG="+dir, "BUILDX_CONFIG="+t.TempDir())
			output, err := command.CombinedOutput()
			t.Logf("fixture CLI %v: %v %s", args, err, output)
		}
		t.Fatalf("complete Docker-loaded image was not reused: %s %t", got, ok)
	}
	if _, ok := preloadedActImage(ctx, Options{ActImage: strings.TrimSuffix(reference, hex.EncodeToString(hash[:])) + strings.Repeat("0", 64), RegistryConfig: dir}); ok {
		t.Fatal("accepted a manifest with a different digest")
	}
}
