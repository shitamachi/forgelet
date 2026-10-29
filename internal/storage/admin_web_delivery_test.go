package storage

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run"
	"github.com/shitamachi/forgelet/internal/workflow"
)

// Use the complete candidate business YAML, including its real CI commands.
// No fake-success CI or independent publisher may bypass the reusable result.
func TestAdminWebReusableCIGatesPublishAndDelivery(t *testing.T) {
	files := map[string][]byte{}
	for _, name := range []string{"ci.yml", "image.yml"} {
		b, err := os.ReadFile(filepath.Join("../../test/fixtures/admin-web-cd", name))
		if err != nil {
			t.Fatal(err)
		}
		files[".github/workflows/"+name] = b
	}
	for _, command := range []string{"npm ci", "npm run check", "npm test", "npm run build", "git diff --exit-code -- src/routeTree.gen.ts"} {
		if !strings.Contains(string(files[".github/workflows/ci.yml"]), command) {
			t.Fatal("original CI command removed", command)
		}
	}
	image := string(files[".github/workflows/image.yml"])
	setup := strings.Index(image, "uses: docker/setup-buildx-action@v3")
	publish := strings.Index(image, "uses: docker/build-push-action@v6")
	if setup < 0 || publish < setup || !strings.Contains(image, "cache-from: type=gha,scope=admin-web") || !strings.Contains(image, "cache-to: type=gha,scope=admin-web,mode=max") || strings.Contains(image, "ignore-error") {
		t.Fatal("standard mirror-aware builder/cache contract missing or failures ignored")
	}
	for _, outcome := range []run.State{run.Succeeded, run.Failed} {
		t.Run(string(outcome), func(t *testing.T) {
			ctx := t.Context()
			event := run.Event{Name: "push", Repository: "shitamachi/steam-admin-web", SHA: strings.Repeat("a", 40), Ref: "refs/heads/main", WorkflowPath: ".github/workflows/image.yml"}
			plans, err := workflow.CompileFiles(files[event.WorkflowPath], event, "admin-web-fixture", files)
			if err != nil {
				t.Fatal(err)
			}
			plans, err = workflow.AttachDeployments(plans, []workflow.DeploymentBinding{{Target: "admin-web", Job: "build-push", Profile: "native"}})
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range plans {
				if p.Key == "build-push" && !slices.Contains(p.Needs, "check") {
					t.Fatal("publisher bypassed reusable CI")
				}
			}
			store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := store.Close(); err != nil {
					t.Error(err)
				}
			}()
			item := run.Run{ID: "admin-web-fixture", DeliveryID: "admin-web-fixture", WorkflowPath: event.WorkflowPath, Repository: event.Repository, SHA: event.SHA, Ref: event.Ref, State: run.Queued, CreatedAt: time.Now()}
			if _, err := store.CreateRun(ctx, item, plans); err != nil {
				t.Fatal(err)
			}
			jobs, err := store.Reserve(ctx, 4)
			if err != nil || len(jobs) != 1 {
				t.Fatalf("expected CI leaf only: %+v %v", jobs, err)
			}
			first := jobs[0]
			if first.Plan.Key == "build-push" || strings.HasPrefix(first.Plan.Key, "forgelet_deploy_") {
				t.Fatal("publish/deploy started before CI")
			}
			if err := store.RecordPod(ctx, first.ID, "ci-uid"); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Claim(ctx, first.ID, "ci-uid"); err != nil {
				t.Fatal(err)
			}
			if err := store.Finish(ctx, first.ID, "ci-uid", run.Result{State: outcome}); err != nil {
				t.Fatal(err)
			}
			jobs, err = store.Reserve(ctx, 4)
			if err != nil {
				t.Fatal(err)
			}
			if outcome == run.Failed {
				if len(jobs) != 0 {
					t.Fatal("failed CI permitted execution", jobs)
				}
				saved, err := store.GetRun(ctx, item.ID)
				if err != nil {
					t.Fatal(err)
				}
				if saved.State != run.Failed {
					t.Fatal(saved.State)
				}
				for _, j := range saved.Jobs {
					if j.Plan.Key == "build-push" || strings.HasPrefix(j.Plan.Key, "forgelet_deploy_") {
						if j.State != run.Skipped || j.PodUID != "" {
							t.Fatal("failed CI did not block publish/CD", j.State)
						}
					}
				}
			} else if len(jobs) != 1 || jobs[0].Plan.Key != "build-push" {
				t.Fatal("successful CI did not release publisher", jobs)
			}
		})
	}
}
