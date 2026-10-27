package actkernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepare(t *testing.T) {
	for _, candidate := range []Candidate{Upstream, Gitea} {
		t.Run(string(candidate), func(t *testing.T) {
			dir := t.TempDir()
			if err := Prepare(dir, candidate); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "replace") != (candidate == Gitea) {
				t.Fatal("candidate module isolation")
			}
		})
	}
	if err := Prepare(t.TempDir(), Candidate("unknown")); err == nil {
		t.Fatal("unknown candidate accepted")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "go.mod")
	if err := os.WriteFile(path, []byte("module user-work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Prepare(dir, Upstream); err == nil {
		t.Fatal("unrelated module overwritten")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "module user-work\n" {
		t.Fatal("unrelated module changed")
	}
}
