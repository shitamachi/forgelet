package workflow

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestAdoptedBusinessPublishersKeepCIGates(t *testing.T) {
	for _, tc := range []struct{ repo, file, publisher, prerequisite string }{
		{"steam-admin", "image.yml", "build-push", "check"},
		{"steam-api", "ci.yml", "build-and-push", "test"},
		{"steam-login", "docker.yml", "publish-custom", "build"},
		{"steam-platform-server", "check.yml", "test", ""},
		{"steam-platform-etl", "check.yml", "test", ""},
		{"steam-tasks", "image.yml", "build-push", "reliability"},
	} {
		t.Run(tc.repo, func(t *testing.T) {
			files := map[string][]byte{}
			names, err := filepath.Glob(filepath.Join("../../test/fixtures/adoption", tc.repo, "*.yml"))
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range names {
				b, err := os.ReadFile(name)
				if err != nil {
					t.Fatal(err)
				}
				files[".github/workflows/"+filepath.Base(name)] = b
			}
			source := files[".github/workflows/"+tc.file]
			plans, err := ValidateFiles(source, run.Event{Name: "push", Repository: "shitamachi/" + tc.repo, Ref: "refs/heads/main", SHA: strings.Repeat("a", 40), WorkflowPath: ".github/workflows/" + tc.file}, files)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, p := range plans {
				if p.Key == tc.publisher {
					found = true
					if tc.prerequisite != "" && !slices.Contains(p.Needs, tc.prerequisite) {
						t.Fatalf("publisher lacks CI dependency: %+v", p.Needs)
					}
				}
			}
			if !found {
				t.Fatal("publisher missing")
			}
			if tc.repo == "steam-api" {
				for _, plan := range plans {
					for _, step := range plan.Steps {
						if strings.Contains(step.Run, "go test") && step.Env["GODEBUG"] != "" {
							t.Fatal("download workaround disables protocols in application tests")
						}
					}
				}
			}
			if tc.repo == "steam-tasks" {
				if !strings.Contains(string(source), "docker/setup-buildx-action@v3") || !strings.Contains(string(source), "cache-to: type=gha,scope=steam-tasks,mode=max") {
					t.Fatal("release build omitted configured mirror/cache builder")
				}
				fullPG := false
				for _, plan := range plans {
					if plan.Key == "reliability/reliability" && plan.Inputs["postgres"] == true {
						for _, step := range plan.Steps {
							if strings.Contains(step.Run, "pg18-workflow") && step.If == "inputs.postgres" {
								fullPG = true
							}
						}
					}
				}
				if !fullPG {
					t.Fatal("release omitted full PostgreSQL acceptance")
				}
			}
			attached, err := AttachDeployments(plans, []DeploymentBinding{{Target: "business", Job: tc.publisher, Profile: "homelab"}})
			if err != nil {
				t.Fatal(err)
			}
			deployment := attached[len(attached)-1]
			if !slices.Contains(deployment.Needs, tc.publisher) {
				t.Fatal("CD bypasses publisher")
			}
			if tc.repo == "steam-platform-etl" && !strings.Contains(string(source), "go list -m -f '{{.Dir}}' github.com/shitamachi/steam-proto-go") {
				t.Fatal("SDK build context is not tied to declared dependency")
			}
		})
	}
}
