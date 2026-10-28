//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	dockercontainer "github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"golang.org/x/crypto/bcrypt"
)

// Exercise the actual candidate ACL against Zot, using only disposable fixture
// passwords. Production htpasswd values and registry credentials are never read.
func TestZotScopedPublisher(t *testing.T) {
	image, path := os.Getenv("FORGELET_TEST_ZOT_IMAGE"), os.Getenv("FORGELET_TEST_ZOT_CONFIG")
	if image == "" || path == "" {
		t.Skip("set FORGELET_TEST_ZOT_IMAGE and FORGELET_TEST_ZOT_CONFIG for the registry ACL fixture")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	raw, err := os.ReadFile(path)
	must(t, err)
	var cfg map[string]any
	must(t, json.Unmarshal(raw, &cfg))
	// Preserve the deployed candidate ACL exactly; isolate storage, credentials,
	// listeners and background extensions from the real registry.
	httpConfig := cfg["http"].(map[string]any)
	httpConfig["address"] = "0.0.0.0"
	httpConfig["port"] = "5000"
	httpConfig["auth"] = map[string]any{"htpasswd": map[string]string{"path": "/tmp/htpasswd"}}
	delete(httpConfig, "tls")
	delete(httpConfig, "externalUrl")
	cfg["storage"] = map[string]any{"rootDirectory": "/tmp/registry", "gc": false}
	cfg["extensions"] = map[string]any{}
	cfg["log"] = map[string]any{"level": "error"}
	raw, err = json.Marshal(cfg)
	must(t, err)
	const publisher = "forgelet-steam-tasks-push"
	const password = "disposable-fixture-password-only"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	must(t, err)
	htpasswd := []byte("admin:" + string(hash) + "\n" + publisher + ":" + string(hash) + "\n")
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{
		Image: image, ExposedPorts: []string{"5000/tcp"}, Cmd: []string{"serve", "/tmp/config.json"},
		Files:              []testcontainers.ContainerFile{{Reader: bytes.NewReader(raw), ContainerFilePath: "/tmp/config.json", FileMode: 0644}, {Reader: bytes.NewReader(htpasswd), ContainerFilePath: "/tmp/htpasswd", FileMode: 0644}},
		HostConfigModifier: func(h *dockercontainer.HostConfig) { h.Memory = 256 << 20; h.NanoCPUs = 500e6 },
		WaitingFor:         wait.ForHTTP("/v2/").WithPort("5000/tcp").WithStatusCodeMatcher(func(status int) bool { return status == http.StatusUnauthorized || status == http.StatusOK }).WithStartupTimeout(time.Minute),
	}, Started: true})
	if c != nil {
		testcontainers.CleanupContainer(t, c)
	}
	must(t, err)
	t.Cleanup(func() {
		if t.Failed() {
			logs, logErr := c.Logs(ctx)
			if logErr == nil {
				defer func() { must(t, logs.Close()) }()
				data, readErr := io.ReadAll(io.LimitReader(logs, 16384))
				if readErr == nil {
					t.Log(string(data))
				}
			}
		}
	})
	endpoint, err := c.Endpoint(ctx, "http")
	must(t, err)
	client := &http.Client{Timeout: 10 * time.Second}
	request := func(method, path, user string, body []byte, status int) http.Header {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, endpoint+path, bytes.NewReader(body))
		must(t, err)
		if user != "" {
			req.SetBasicAuth(user, password)
		}
		if strings.Contains(path, "/manifests/") {
			req.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		} else {
			req.Header.Set("Content-Type", "application/octet-stream")
		}
		resp, err := client.Do(req)
		must(t, err)
		defer func() { must(t, resp.Body.Close()) }()
		data, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
		must(t, err)
		if resp.StatusCode != status {
			t.Fatalf("%s %s as %q: status=%d want=%d body=%s", method, path, user, resp.StatusCode, status, data)
		}
		return resp.Header
	}
	const repo = "/v2/forgelet/apps/steam-task"
	request(http.MethodPost, repo+"/blobs/uploads/", "", nil, http.StatusUnauthorized)
	request(http.MethodPost, "/v2/forgelet/server/blobs/uploads/", publisher, nil, http.StatusForbidden)
	request(http.MethodPost, "/v2/forgelet/dockerhub/library/alpine/blobs/uploads/", publisher, nil, http.StatusForbidden)
	request(http.MethodGet, "/v2/forgelet/dockerhub/library/alpine/manifests/nonexistent", "", nil, http.StatusNotFound)
	// Admin retains access to other paths; the CI user never receives admin rights.
	request(http.MethodPost, "/v2/forgelet/other/blobs/uploads/", "admin", nil, http.StatusAccepted)
	header := request(http.MethodPost, repo+"/blobs/uploads/", publisher, nil, http.StatusAccepted)
	location, err := url.Parse(header.Get("Location"))
	must(t, err)
	config := []byte("{}")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(config))
	query := location.Query()
	query.Set("digest", digest)
	location.RawQuery = query.Encode()
	request(http.MethodPut, location.RequestURI(), publisher, config, http.StatusCreated)
	var finalDigest string
	for _, version := range []string{"first", "second"} {
		manifest := map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json", "config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": digest, "size": len(config)}, "layers": []any{}, "annotations": map[string]string{"fixture-version": version}}
		body, err := json.Marshal(manifest)
		must(t, err)
		finalDigest = fmt.Sprintf("sha256:%x", sha256.Sum256(body))
		request(http.MethodPut, repo+"/manifests/latest", publisher, body, http.StatusCreated)
		h := request(http.MethodGet, repo+"/manifests/latest", publisher, nil, http.StatusOK)
		if h.Get("Docker-Content-Digest") != finalDigest {
			t.Fatal("latest did not point to the pushed manifest")
		}
	}
	request(http.MethodGet, repo+"/manifests/latest", "admin", nil, http.StatusOK)
	request(http.MethodGet, repo+"/manifests/latest", "", nil, http.StatusUnauthorized)
	request(http.MethodDelete, repo+"/manifests/"+finalDigest, publisher, nil, http.StatusForbidden)
}
