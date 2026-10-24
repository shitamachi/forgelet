package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/delivery"
)

func getManagement(t *testing.T, h *harness) Management {
	t.Helper()
	code, body := h.admin("GET", "/api/management", nil)
	if code != 200 {
		t.Fatalf("get %d %s", code, body)
	}
	var m Management
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	return m
}
func putManagement(t *testing.T, h *harness, m Management, want int) Management {
	t.Helper()
	code, body := h.admin("PUT", "/api/management", m)
	if code != want {
		t.Fatalf("save %d expected %d: %s", code, want, body)
	}
	if want == 200 {
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatal(err)
		}
	}
	return m
}
func TestManagementPersistenceConflictAndAdmission(t *testing.T) {
	h := newHarness(t, 2)
	m := getManagement(t, h)
	stale := m
	m.Repositories = append(m.Repositories, RepositoryConfig{Name: "me/second", InstallationID: 42, Enabled: true})
	m = putManagement(t, h, m, 200)
	if !h.server.allowed("me/second") {
		t.Fatal("saved repository not active")
	}
	putManagement(t, h, stale, 409)
	h.http.Close()
	h.start()
	if got := getManagement(t, h); got.Version != m.Version || !h.server.allowed("me/second") {
		t.Fatalf("restart lost configuration: %+v", got)
	}
	if code, _ := h.webhook("before-disable", nil); code != 202 {
		t.Fatal(code)
	}
	m.Repositories[0].Enabled = false
	m = putManagement(t, h, m, 200)
	h.tick()
	runs, err := h.store.ListRuns(t.Context())
	if err != nil || len(runs) != 0 {
		t.Fatalf("pending disabled event executed: %v %v", runs, err)
	}
	if code, _ := h.webhook("after-disable", nil); code != 403 {
		t.Fatalf("disabled webhook: %d", code)
	}
	m.Repositories[0].Enabled = true
	putManagement(t, h, m, 200)
	h.manual(testWorkflow) // Restored admission; still uses ordinary workflow validation.
}
func TestManagementWorkflowRulesExecuteAndDisable(t *testing.T) {
	h := newHarness(t, 2)
	m := getManagement(t, h)
	m.Workflows = []WorkflowConfig{{Repository: "me/repo", Path: ".github/workflows/custom.yml", Enabled: true, Source: strings.ReplaceAll(testWorkflow, "echo hello", "echo managed-source")}}
	m = putManagement(t, h, m, 200)
	if code, _ := h.webhook("managed-event", nil); code != 202 {
		t.Fatal(code)
	}
	h.tick()
	runs, err := h.store.ListRuns(t.Context())
	if err != nil || len(runs) != 1 || runs[0].WorkflowPath != ".github/workflows/custom.yml" || !strings.Contains(runs[0].Jobs[0].Plan.Steps[0].Run, "managed-source") {
		t.Fatalf("managed source not used: %+v %v", runs, err)
	}
	m.Workflows[0].Enabled = false
	putManagement(t, h, m, 200)
	if code, _ := h.webhook("disabled-workflow", nil); code != 202 {
		t.Fatal(code)
	}
	h.tick()
	runs, err = h.store.ListRuns(t.Context())
	if err != nil || len(runs) != 1 {
		t.Fatalf("disabled workflow executed: %d %v", len(runs), err)
	}
}
func TestManagementRejectsInvalidAndCrossOriginDocuments(t *testing.T) {
	h := openHarness(t)
	for _, tc := range []struct {
		name   string
		modify func(*Management)
	}{
		{"duplicate repo", func(m *Management) { m.Repositories = append(m.Repositories, m.Repositories[0]) }},
		{"bad repo", func(m *Management) { m.Repositories[0].Name = "../repo" }},
		{"bad installation", func(m *Management) { m.Repositories[0].InstallationID = -1 }},
		{"unknown repo", func(m *Management) {
			m.Workflows = []WorkflowConfig{{Repository: "other/repo", Path: ".github/workflows/a.yml"}}
		}},
		{"unsafe path", func(m *Management) {
			m.Workflows = []WorkflowConfig{{Repository: "me/repo", Path: ".github/workflows/../x.yml"}}
		}},
		{"filtered invalid job", func(m *Management) {
			m.Workflows = []WorkflowConfig{{Repository: "me/repo", Path: ".github/workflows/a.yml", Source: "on: {push: {branches: [other]}}\njobs: {a: {runs-on: small, unsupported: true}}"}}
		}},
		{"invalid YAML", func(m *Management) {
			m.Workflows = []WorkflowConfig{{Repository: "me/repo", Path: ".github/workflows/a.yml", Source: "["}}
		}},
		{"missing adapter", func(m *Management) { m.Targets = []delivery.Target{{Name: "bad"}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := getManagement(t, h)
			tc.modify(&m)
			putManagement(t, h, m, 400)
			if getManagement(t, h).Version != 0 {
				t.Fatal("invalid configuration persisted")
			}
		})
	}
	code, _ := h.request("PUT", "/api/management", `{}`, map[string]string{"Content-Type": "application/json", "Origin": "https://evil.invalid"})
	if code != 403 {
		t.Fatal(code)
	}
	code, _ = h.request("PUT", "/api/management", `{"unknown":1}`, map[string]string{"Content-Type": "application/json"})
	if code != 400 {
		t.Fatal(code)
	}
}
func TestManagementTargetsPersistWithoutChangingDeliverySnapshots(t *testing.T) {
	h := newHarness(t, 2)
	h.http.Close()
	h.cfg.Delivery = &delivery.Coordinator{Store: h.store, Now: time.Now}
	h.start()
	target := delivery.Target{Name: "app", SourceRepository: "me/repo", SourceRef: "refs/heads/main", Repository: "me/gitops", InstallationID: 42, Branch: "main", Path: "apps/kustomization.yaml", Mode: "direct", Image: "ghcr.io/me/app", ImageName: "app", FluxNamespace: "flux-system", Kustomization: "app", Namespace: "apps", Deployment: "app", Container: "app"}
	m := getManagement(t, h)
	m.Targets = []delivery.Target{target}
	m = putManagement(t, h, m, 200)
	got, ok := h.server.target("app")
	if !ok || got != target {
		t.Fatal(got)
	}
	item := h.manual(testWorkflow)
	h.tick()
	job, err := h.store.GetJob(t.Context(), item.Jobs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := h.runtimeRequest("POST", "claim", job, nil); code != 200 {
		t.Fatal(code)
	}
	if code, body := h.runtimeRequest("POST", "deployments", job, map[string]string{"target": "app", "digest": "sha256:" + strings.Repeat("a", 64)}); code != 200 {
		t.Fatal(code, string(body))
	}
	m.Targets[0].Branch = "production"
	putManagement(t, h, m, 200)
	records, err := h.store.Deliveries(t.Context(), false)
	if err != nil || len(records) != 1 || records[0].Target.Branch != "main" {
		t.Fatalf("changed durable intent: %+v %v", records, err)
	}
	h.http.Close()
	h.start()
	got, ok = h.server.target("app")
	if !ok || got.Branch != "production" {
		t.Fatal("target lost after restart")
	}
}
func TestManagementInspectsFixedCommitWithRegisteredInstallation(t *testing.T) {
	h := newHarness(t, 2)
	m := getManagement(t, h)
	m.Repositories[0].InstallationID = 42
	putManagement(t, h, m, 200)
	code, body := h.admin("POST", "/api/management/inspect", map[string]string{"repository": "me/repo", "sha": testSHA})
	if code != 200 || !strings.Contains(string(body), "ci.yml") {
		t.Fatal(code, string(body))
	}
	code, _ = h.admin("POST", "/api/management/inspect", map[string]string{"repository": "other/repo", "sha": testSHA})
	if code != 400 {
		t.Fatal(code)
	}
}

func TestManagementSnapshotIsNotChangedByConcurrentEditors(t *testing.T) {
	h := newHarness(t, 2)
	m := getManagement(t, h)
	first := m
	first.Repositories = append([]RepositoryConfig{}, m.Repositories...)
	first.Repositories[0].InstallationID = 42
	stale := m
	stale.Repositories = append([]RepositoryConfig{}, m.Repositories...)
	stale.Repositories[0].Enabled = false
	putManagement(t, h, first, 200)
	putManagement(t, h, stale, 409)
	current := getManagement(t, h)
	if current.Repositories[0].InstallationID != 42 || !current.Repositories[0].Enabled {
		t.Fatal(current)
	}
}

func TestManagementMissingGitWorkflowHasDiagnostic(t *testing.T) {
	h := newHarness(t, 2)
	m := getManagement(t, h)
	m.Workflows = []WorkflowConfig{{Repository: "me/repo", Path: ".github/workflows/missing.yml", Enabled: true}}
	putManagement(t, h, m, 200)
	if code, _ := h.webhook("missing-git-file", nil); code != 202 {
		t.Fatal(code)
	}
	h.tick()
	events, err := h.store.Events(t.Context())
	if err != nil || len(events) != 1 || !strings.Contains(events[0].Error, "absent at event SHA") {
		t.Fatal(events, err)
	}
}
