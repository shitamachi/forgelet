package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/delivery"
	"github.com/shitamachi/forgelet/internal/kube"
	"github.com/shitamachi/forgelet/internal/run"
)

const automaticWorkflow = `on: [push, workflow_dispatch, pull_request]
jobs:
  check:
    runs-on: small
    steps:
      - run: echo checked
  image:
    needs: check
    runs-on: actions
    steps:
      - uses: docker/build-push-action@v6
        with: {push: true}
`

func TestAutomaticDeploymentAdmissionScopeAndSQLiteRestart(t *testing.T) {
	h := newHarness(t, 2)
	h.http.Close()
	h.cfg.Profiles["actions"] = kube.Profile{Image: "executor:act", Act: &kube.ActProfile{}}
	target := delivery.Target{Name: "web", SourceRepository: "me/repo", SourceRef: "refs/heads/main", Repository: "me/gitops", InstallationID: 42, Branch: "main", Path: "apps/kustomization.yaml", Mode: "direct", Image: "ghcr.io/me/app", ImageName: "app", FluxNamespace: "flux-system", Kustomization: "app", Namespace: "apps", Deployment: "app", Container: "app", OnSuccess: &delivery.OnSuccess{WorkflowPath: ".github/workflows/ci.yml", Job: "image", Profile: "small"}}
	h.cfg.Targets = map[string]delivery.Target{"web": target}
	h.cfg.Delivery = &delivery.Coordinator{Store: h.store, Now: time.Now}
	h.start()
	for _, profile := range []string{"missing", "actions"} {
		m := getManagement(t, h)
		m.Targets[0].OnSuccess = &delivery.OnSuccess{WorkflowPath: ".github/workflows/ci.yml", Job: "image", Profile: profile}
		putManagement(t, h, m, 400)
	}
	for _, tc := range []struct {
		name, event, ref, path string
		jobs                   int
	}{
		{"push", "push", "refs/heads/main", ".github/workflows/ci.yml", 3},
		{"default push", "", "refs/heads/main", ".github/workflows/ci.yml", 3},
		{"other branch", "push", "refs/heads/develop", ".github/workflows/ci.yml", 2},
		{"other file", "push", "refs/heads/main", ".github/workflows/image.yml", 2},
		{"manual", "workflow_dispatch", "refs/heads/main", ".github/workflows/ci.yml", 2},
		{"pull request", "pull_request", "refs/pull/1/merge", ".github/workflows/ci.yml", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := h.admin("POST", "/api/runs", map[string]any{"repository": "me/repo", "sha": testSHA, "ref": tc.ref, "workflow_path": tc.path, "event_name": tc.event, "workflow": automaticWorkflow, "installation_id": 42})
			if tc.event == "pull_request" {
				if code != 400 {
					t.Fatal("manual API accepted PR", code)
				}
				p := []run.Plan{{Key: "image"}}
				got, err := h.server.attachDeployments(p, run.Event{Name: "pull_request", Repository: "me/repo", Ref: target.SourceRef, WorkflowPath: target.OnSuccess.WorkflowPath})
				if err != nil || len(got) != 1 {
					t.Fatal("PR attached a deployment", got, err)
				}
				return
			}
			if code != 201 {
				t.Fatalf("%d %s", code, body)
			}
			var item run.Run
			if err := json.Unmarshal(body, &item); err != nil {
				t.Fatal(err)
			}
			if len(item.Jobs) != tc.jobs {
				t.Fatal(item)
			}
			if tc.jobs == 3 {
				// Reload the server against the same real SQLite store. The persisted
				// graph must survive even when automatic policy is subsequently removed.
				h.http.Close()
				delete(h.cfg.Targets, "web")
				h.start()
				saved, err := h.store.GetRun(t.Context(), item.ID)
				if err != nil || len(saved.Jobs) != 3 {
					t.Fatal(saved, err)
				}
				var deploy run.Job
				for _, j := range saved.Jobs {
					if strings.HasPrefix(j.Plan.Key, "forgelet_deploy_") {
						deploy = j
					}
				}
				if deploy.Plan.Profile != "small" || deploy.Plan.Kernel != "" || len(deploy.Plan.Needs) != 2 || deploy.Plan.Steps[0].Action.Inputs["target"] != "web" {
					t.Fatal(deploy)
				}
				// Cancelling the run also cancels its queued deployment.
				if code, body := h.admin("POST", "/api/runs/"+item.ID+"/cancel", nil); code != 200 {
					t.Fatal(code, string(body))
				}
				j, err := h.store.GetJob(t.Context(), deploy.ID)
				if err != nil || j.State != run.Cancelled {
					t.Fatal(j, err)
				}
				h.http.Close()
				h.cfg.Targets["web"] = target
				h.start()
			}
		})
	}
}
