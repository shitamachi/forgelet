//go:build integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// This creates a disposable Git fixture object, never a commit in the owner's
// repository. Original YAML stays byte-identical; source corrections are explicit.
func preparePlatformWebFixture(t *testing.T, ctx context.Context, root, source, base string) (string, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "source")
	businessCommand(t, ctx, "git", "clone", "--no-hardlinks", "--no-checkout", source, dir)
	businessCommand(t, ctx, "git", "-C", dir, "checkout", "--detach", base)
	patch, err := os.ReadFile(filepath.Join(root, "test/fixtures/steam-build/steam-platform-web/hydration.patch"))
	must(t, err)
	command := exec.CommandContext(ctx, "git", "-C", dir, "apply", "--index", "-")
	command.Stdin = strings.NewReader(string(patch))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("apply explicit platform-web source correction: %v: %s", err, output)
	}
	allowed := map[string]bool{"src/shared/state.tsx": true, "src/shared/components.tsx": true, "src/shared/state.test.tsx": true}
	for _, path := range strings.Fields(businessCommand(t, ctx, "git", "-C", dir, "diff", "--cached", "--name-only")) {
		if !allowed[path] {
			t.Fatalf("source correction changes unexpected path %q", path)
		}
	}
	tree := strings.TrimSpace(businessCommand(t, ctx, "git", "-C", dir, "write-tree"))
	command = exec.CommandContext(ctx, "git", "-C", dir, "commit-tree", tree, "-p", base)
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Forgelet integration fixture", "GIT_AUTHOR_EMAIL=fixture@forgelet.invalid", "GIT_COMMITTER_NAME=Forgelet integration fixture", "GIT_COMMITTER_EMAIL=fixture@forgelet.invalid", "GIT_AUTHOR_DATE=2026-10-27T00:00:00Z", "GIT_COMMITTER_DATE=2026-10-27T00:00:00Z")
	command.Stdin = strings.NewReader("test: platform-web storage hydration regression fixture\n")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("write disposable source fixture: %v: %s", err, output)
	}
	commit := strings.TrimSpace(string(output))
	businessCommand(t, ctx, "git", "-C", dir, "reset", "--hard", commit)
	t.Logf("explicit platform-web correction: base=%s fixture=%s patch_sha256=%x (original workflow unchanged)", base, commit, sha256.Sum256(patch))
	return dir, commit
}

func TestPlatformWebSourceFixtureKeepsWorkflowAndOwnerRepository(t *testing.T) {
	ctx := t.Context()
	source := t.TempDir()
	businessCommand(t, ctx, "git", "init", source)
	businessCommand(t, ctx, "git", "-C", source, "config", "user.name", "fixture")
	businessCommand(t, ctx, "git", "-C", source, "config", "user.email", "fixture@forgelet.invalid")
	must(t, os.MkdirAll(filepath.Join(source, "src/shared"), 0700))
	must(t, os.MkdirAll(filepath.Join(source, ".github/workflows"), 0700))
	must(t, os.WriteFile(filepath.Join(source, "src/shared/state.tsx"), []byte("old\n"), 0600))
	workflow := []byte("on: push\njobs: {}\n")
	must(t, os.WriteFile(filepath.Join(source, ".github/workflows/check.yml"), workflow, 0600))
	businessCommand(t, ctx, "git", "-C", source, "add", ".")
	businessCommand(t, ctx, "git", "-C", source, "commit", "-m", "fixture parent")
	base := strings.TrimSpace(businessCommand(t, ctx, "git", "-C", source, "rev-parse", "HEAD"))
	must(t, os.WriteFile(filepath.Join(source, "owner-untracked"), []byte("preserve"), 0600))
	before := businessCommand(t, ctx, "git", "-C", source, "status", "--porcelain")
	root := t.TempDir()
	path := filepath.Join(root, "test/fixtures/steam-build/steam-platform-web")
	must(t, os.MkdirAll(path, 0700))
	patch := "diff --git a/src/shared/state.tsx b/src/shared/state.tsx\n--- a/src/shared/state.tsx\n+++ b/src/shared/state.tsx\n@@ -1 +1 @@\n-old\n+new\n"
	must(t, os.WriteFile(filepath.Join(path, "hydration.patch"), []byte(patch), 0600))
	fixed, commit := preparePlatformWebFixture(t, ctx, root, source, base)
	_, repeated := preparePlatformWebFixture(t, ctx, root, source, base)
	if commit == base || commit != repeated {
		t.Fatal("fixture is not independently reproducible", base, commit, repeated)
	}
	if got := businessCommand(t, ctx, "git", "-C", source, "status", "--porcelain"); got != before {
		t.Fatal("owner changes modified", got, before)
	}
	if got := strings.TrimSpace(businessCommand(t, ctx, "git", "-C", source, "rev-parse", "HEAD")); got != base {
		t.Fatal("owner HEAD changed")
	}
	data, err := os.ReadFile(filepath.Join(source, "src/shared/state.tsx"))
	must(t, err)
	if string(data) != "old\n" {
		t.Fatal("owner source changed")
	}
	data, err = os.ReadFile(filepath.Join(fixed, "src/shared/state.tsx"))
	must(t, err)
	if string(data) != "new\n" {
		t.Fatal("correction absent")
	}
	data, err = os.ReadFile(filepath.Join(fixed, ".github/workflows/check.yml"))
	must(t, err)
	if string(data) != string(workflow) {
		t.Fatal("original workflow changed")
	}
	if got := businessCommand(t, ctx, "git", "-C", fixed, "status", "--porcelain"); got != "" {
		t.Fatal("fixture is dirty", got)
	}
}
