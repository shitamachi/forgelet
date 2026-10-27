package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestOriginalBusinessWorkflowsKeepSourceAndActionRequirements(t *testing.T) {
	base := "../../test/fixtures/steam-business"
	data, err := os.ReadFile(filepath.Join(base, "sources.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sources map[string]struct {
		Repository string `json:"repository"`
		Commit     string `json:"commit"`
		Path       string `json:"path"`
		SHA256     string `json:"sha256"`
	}
	if err := json.Unmarshal(data, &sources); err != nil {
		t.Fatal(err)
	}
	for name, source := range sources {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(base, name))
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(data)
			if hex.EncodeToString(sum[:]) != source.SHA256 {
				t.Fatal("original workflow changed")
			}
			plans, err := Compile(data, run.Event{Repository: source.Repository, SHA: source.Commit, WorkflowPath: source.Path, Name: "push", Ref: "refs/heads/main"}, "business")
			if err != nil || len(plans) != 1 {
				t.Fatal(plans, err)
			}
			if name == "steam-admin-web/ci.yml" {
				found := false
				for _, step := range plans[0].Steps {
					if step.Action != nil && step.Action.Uses == "actions/setup-node@v4" {
						found = true
						if step.Action.SHA != "" || step.Action.Ref != "v4" || step.Action.Inputs["cache"] != "npm" || step.Action.Inputs["node-version"] != "24" {
							t.Fatal(step.Action)
						}
					}
				}
				if !found || len(plans[0].Steps) != 7 {
					t.Fatal("original npm/build/diff steps lost", plans)
				}
			}
		})
	}
}
