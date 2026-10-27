package noderuntime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSelectDeclaredRuntime(t *testing.T) {
	for _, runtime := range []string{"node20", "node24", "node16", "composite"} {
		t.Run(runtime, func(t *testing.T) {
			dir := t.TempDir()
			script := filepath.Join(dir, "dist", "main.js")
			if err := os.MkdirAll(filepath.Dir(script), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(script, []byte("console.log(process.versions.node)"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "action.yml"), []byte("runs:\n  using: "+runtime+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := Select([]string{script}, dir)
			if runtime == "node16" {
				if err == nil {
					t.Fatal("legacy runtime silently substituted")
				}
				return
			}
			want := runtime
			if runtime == "composite" {
				want = "node24"
			}
			if err != nil || got != want {
				t.Fatal(got, err)
			}
		})
	}
	if got, err := Select([]string{"-e", "console.log('inline')"}, ""); err != nil || got != "node24" {
		t.Fatal(got, err)
	}
}
