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

func TestOriginalBuildWorkflowsAndPackagePermissions(t *testing.T) {
	base := "../../test/fixtures/steam-build"
	data, err := os.ReadFile(filepath.Join(base, "sources.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sources map[string]struct{ Repository, Commit, Path, SHA256 string }
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
				t.Fatal("original bytes changed")
			}
			plans, err := Compile(data, run.Event{Repository: source.Repository, SHA: source.Commit, WorkflowPath: source.Path, Name: "push", Ref: "refs/heads/main"}, "original-build")
			if err != nil {
				t.Fatal(err)
			}
			if len(plans) == 0 {
				t.Fatal("missing jobs")
			}
			if name == "steam-login/docker.yml" && (plans[0].Permissions["packages"] != "write" || plans[0].Permissions["contents"] != "read") {
				t.Fatal(plans)
			}
		})
	}
}
func TestReusableWorkflowCannotEscalatePackagePermissions(t *testing.T) {
	for _, parent := range []map[string]string{{"contents": "read"}, {"contents": "read", "packages": "read"}, {}} {
		if err := permissionCeiling(map[string]string{"contents": "read", "packages": "write"}, parent); err == nil {
			t.Fatal("packages permission escalated", parent)
		}
	}
	if err := permissionCeiling(map[string]string{"contents": "read", "packages": "write"}, map[string]string{"contents": "read", "packages": "write"}); err != nil {
		t.Fatal(err)
	}
}
