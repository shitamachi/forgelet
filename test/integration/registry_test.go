//go:build integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	dockerclient "github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
	"k8s.io/client-go/kubernetes"
)

// Keep registry/build semantics real without making Docker Hub availability a
// dependency of every attempt. Only a cached, fixed-version test image is copied.
func pushTestImage(t *testing.T, ctx context.Context, cluster testcontainers.Container, client kubernetes.Interface, source, name string) string {
	t.Helper()
	external, err := cluster.PortEndpoint(ctx, "30500/tcp", "http")
	must(t, err)
	probe := &http.Client{Timeout: 3 * time.Second}
	waitFor(t, ctx, "test registry NodePort serves HTTP", func() bool {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, external+"/v2/", nil)
		if err != nil {
			return false
		}
		resp, err := probe.Do(req)
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusOK
	})
	target := strings.TrimPrefix(external, "http://") + "/" + name
	docker, err := testcontainers.NewDockerClientWithOpts(ctx)
	must(t, err)
	defer func() { _ = docker.Close() }()
	_, err = docker.ImageTag(ctx, dockerclient.ImageTagOptions{Source: source, Target: target})
	must(t, err)
	defer func() {
		_, _ = docker.ImageRemove(context.WithoutCancel(ctx), target, dockerclient.ImageRemoveOptions{})
	}()
	auth := base64.URLEncoding.EncodeToString([]byte(`{"username":"test","password":"integration-registry-password"}`))
	push, err := docker.ImagePush(ctx, target, dockerclient.ImagePushOptions{RegistryAuth: auth})
	must(t, err)
	must(t, push.Wait(ctx))
	return strings.SplitN(registryImage(t, ctx, client), "/", 2)[0] + "/" + name
}

// Resolve the exact uploaded manifest through the authenticated registry, rather
// than guessing a digest from a tag or the Docker config identity.
func pinTestRegistryImage(t *testing.T, ctx context.Context, cluster testcontainers.Container, tag string) string {
	t.Helper()
	external, err := cluster.PortEndpoint(ctx, "30500/tcp", "http")
	must(t, err)
	slash := strings.IndexByte(tag, '/')
	repoTag := tag[slash+1:]
	colon := strings.LastIndexByte(repoTag, ':')
	repo, version := repoTag[:colon], repoTag[colon+1:]
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, external+"/v2/"+repo+"/manifests/"+version, nil)
	must(t, err)
	req.SetBasicAuth("test", "integration-registry-password")
	req.Header.Set("Accept", "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json")
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	must(t, err)
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("manifest lookup: %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 128<<10))
	must(t, err)
	sum := sha256.Sum256(body)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if response.Header.Get("Docker-Content-Digest") != digest {
		t.Fatal("registry digest differs from actual manifest")
	}
	return fmt.Sprintf("%s/%s@%s", tag[:slash], repo, digest)
}
