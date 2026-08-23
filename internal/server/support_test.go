package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ConfigMap volumes front their files with a ..data symlink; the workflow
// directory loader must follow symlinks and skip non-regular entries.
func TestReadDirSortedFollowsConfigMapSymlinks(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "..data")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "ci.yml"), []byte("on: push\njobs:\n  a:\n    runs-on: x\n    steps:\n      - run: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ci.yml", "..data"} {
		if err := os.Symlink(filepath.Join("..data", strings.TrimPrefix(name, "..")), filepath.Join(dir, name)); err != nil && !os.IsExist(err) {
			t.Fatal(err)
		}
	}
	files, err := readDirSorted(dir)
	if err != nil {
		t.Fatalf("readDirSorted: %v", err)
	}
	if len(files) != 1 || files[0].name != "ci.yml" || !strings.Contains(string(files[0].data), "jobs:") {
		t.Errorf("files = %+v", files)
	}
}
