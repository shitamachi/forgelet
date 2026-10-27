package server

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/blob"
	"github.com/shitamachi/forgelet/internal/delivery"
	"github.com/shitamachi/forgelet/internal/run"
)

func TestRuntimeBlobsAreAuthenticatedAndScoped(t *testing.T) {
	h := newHarness(t, 4)
	h.http.Close()
	var err error
	h.cfg.Blobs, err = blob.Open(filepath.Join(t.TempDir(), "blobs"), blob.Limits{ObjectBytes: 1024, CacheObjectBytes: 2048, TotalBytes: 16384, Objects: 20})
	if err != nil {
		t.Fatal(err)
	}
	h.start()
	first := h.manual(testWorkflow)
	second := h.manual(testWorkflow)
	h.tick()
	claim := func(item run.Run) run.Job {
		job, err := h.store.GetJob(t.Context(), item.Jobs[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		if code, body := h.runtimeRequest("POST", "claim", job, nil); code != 200 {
			t.Fatal(code, string(body))
		}
		return job
	}
	j1, j2 := claim(first), claim(second)
	put := func(job run.Job, key, raw string) (int, []byte) {
		return h.request("PUT", "/internal/jobs/"+job.ID+"/blobs/artifact?key="+key, raw, map[string]string{"Authorization": "Bearer token-" + job.ID})
	}
	if code, body := put(j1, "dist", "archive"); code != 200 {
		t.Fatal(code, string(body))
	}
	if code, _ := put(j1, "dist", "changed"); code != 409 {
		t.Fatal("overwrote immutable artifact", code)
	}
	if code, body := h.runtimeRequest("GET", "blobs/artifact?key=dist", j1, nil); code != 200 || string(body) != "archive" {
		t.Fatal(code, string(body))
	}
	if code, _ := h.runtimeRequest("GET", "blobs/artifact?key=dist", j2, nil); code != 404 {
		t.Fatal("cross-run read", code)
	}
	if code, _ := h.request("GET", "/internal/jobs/"+j1.ID+"/blobs/artifact?key=dist", "", map[string]string{"Authorization": "Bearer token-" + j2.ID}); code != 401 {
		t.Fatal("cross-job identity", code)
	}
	if code, _ := put(j1, "large", strings.Repeat("x", 1025)); code != 413 {
		t.Fatal("quota", code)
	}
	cachePath := "/internal/jobs/" + j1.ID + "/blobs/cache?version=" + strings.Repeat("a", 64) + "&key="
	for _, tc := range []struct {
		key          string
		size, status int
	}{{"larger-cache", 2048, 200}, {"too-large-cache", 2049, 413}} {
		if code, body := h.request("PUT", cachePath+tc.key, strings.Repeat("x", tc.size), map[string]string{"Authorization": "Bearer token-" + j1.ID}); code != tc.status {
			t.Fatal(tc.key, code, string(body))
		}
	}
	if code, _ := h.runtimeRequest("GET", "blobs/cache?key=test", j1, nil); code != 400 {
		t.Fatal("cache without version", code)
	}
	h.complete(j1)
	if code, _ := put(j1, "late", "archive"); code != 409 {
		t.Fatal("terminal upload", code)
	}
	if code, _ := h.admin("GET", "/api/runs/"+first.ID+"/artifacts/dist", nil); code != 200 {
		t.Fatal("admin artifact", code)
	}
}

func TestDeliveryRequestsPersistAndEnforceTargetScope(t *testing.T) {
	h := newHarness(t, 2)
	h.http.Close()
	target := delivery.Target{Name: "app", SourceRepository: "me/repo", SourceRef: "refs/heads/main", Repository: "me/gitops", InstallationID: 42, Branch: "main", Path: "apps/kustomization.yaml", Mode: "direct", Image: "ghcr.io/me/app", ImageName: "app", FluxNamespace: "flux-system", Kustomization: "app", Namespace: "apps", Deployment: "app", Container: "app"}
	h.cfg.Targets = map[string]delivery.Target{"app": target}
	h.cfg.Delivery = &delivery.Coordinator{Store: h.store, Now: time.Now}
	h.start()
	item := h.manual(testWorkflow)
	h.tick()
	job, err := h.store.GetJob(t.Context(), item.Jobs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := h.runtimeRequest("POST", "claim", job, nil); code != 200 {
		t.Fatal(code)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, test := range []struct {
		target, digest string
		status         int
	}{{"unknown", digest, 403}, {"app", "latest", 400}, {"app", digest, 200}, {"app", digest, 200}, {"app", "sha256:" + strings.Repeat("b", 64), 409}} {
		if code, body := h.runtimeRequest("POST", "deployments", job, map[string]string{"target": test.target, "digest": test.digest}); code != test.status {
			t.Fatal(code, string(body))
		}
	}
	var saved delivery.Record
	code, body := h.admin("GET", "/api/deployments/"+delivery.ID(item.ID, "app"), nil)
	if code != 200 {
		t.Fatal(code, string(body))
	}
	if err := json.Unmarshal(body, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.State != delivery.Queued || saved.Digest != digest {
		t.Fatal(saved)
	}
	h.server.cfg.Targets["app"] = delivery.Target{SourceRepository: "another/repo"}
	if code, _ := h.runtimeRequest("POST", "deployments", job, map[string]string{"target": "app", "digest": digest}); code != 403 {
		t.Fatal("target scope", code)
	}
}
