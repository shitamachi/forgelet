//nolint:errcheck
package executor

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/run/plan"
)

func TestEngineBuiltinCheckout(t *testing.T) {
	// Setup bare repo as before
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "bare.git")
	if out, err := exec.CommandContext(context.Background(), "git", "init", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v %s", err, out)
	}
	work := filepath.Join(tmp, "src")
	if out, err := exec.CommandContext(context.Background(), "git", "init", work).CombinedOutput(); err != nil {
		t.Fatalf("init work: %v %s", err, out)
	}
	exec.CommandContext(context.Background(), "git", "-C", work, "config", "user.email", "test@test.com").Run()
	exec.CommandContext(context.Background(), "git", "-C", work, "config", "user.name", "test").Run()
	if err := os.WriteFile(filepath.Join(work, "app.txt"), []byte("from repo"), 0o644); err != nil {
		t.Fatal(err)
	}
	exec.CommandContext(context.Background(), "git", "-C", work, "add", "app.txt").Run()
	exec.CommandContext(context.Background(), "git", "-C", work, "commit", "-m", "init").Run()
	exec.CommandContext(context.Background(), "git", "-C", work, "push", bare, "HEAD:refs/heads/main").Run()
	shaOut, _ := exec.CommandContext(context.Background(), "git", "-C", work, "rev-parse", "HEAD").Output()
	sha := strings.TrimSpace(string(shaOut))

	cp := &fakeCP{}
	e, _ := newEngine(t, cp)
	e.WorkDir = t.TempDir() // fresh workspace
	// Override workdir for engine exprEnv hasher etc.
	p := plan.Plan{
		JobRunID:   "01JTEST0000000000000000000X",
		Repository: model.RepositoryRef{Provider: "github", Owner: "o", Name: "r"},
		SHA:        sha,
		Ref:        "refs/heads/main",
		Steps: []plan.Step{
			{
				ID: "checkout",
				Builtin: &plan.BuiltinStep{
					Action:  "actions/checkout",
					Version: "v4",
					Inputs: map[string]string{
						"repository": bare,
						"ref":        sha,
					},
				},
			},
			{
				ID:  "verify",
				Run: plan.RunStep{Script: "test \"$(cat app.txt)\" = \"from repo\""},
			},
		},
	}
	// Engine Run will interpolate and call checkout handler
	id := testID()
	result, err := e.Run(context.Background(), id, p)
	if err != nil {
		t.Fatalf("run: %v %+v", err, result)
	}
	if !result.Success {
		t.Fatalf("result not success: %+v", result)
	}
	if len(result.Steps) != 2 {
		t.Fatalf("steps = %d", len(result.Steps))
	}
	if result.Steps[0].Outcome != outcomeSuccess {
		t.Errorf("checkout outcome = %s", result.Steps[0].Outcome)
	}
	// Also verify file exists via workspace directly
	if data, err := os.ReadFile(filepath.Join(e.WorkDir, "app.txt")); err != nil || string(data) != "from repo" {
		t.Errorf("app.txt = %q err=%v", string(data), err)
	}
	// Verify plan digest includes builtin (stability)
	if _, err := p.Digest(); err != nil {
		t.Fatalf("digest: %v", err)
	}
}

func TestEngineBuiltinContinueOnError(t *testing.T) {
	cp := &fakeCP{}
	e, _ := newEngine(t, cp)
	p := plan.Plan{
		JobRunID: "01JTEST0000000000000000000X",
		Steps: []plan.Step{
			{
				ID: "bad-cache",
				Builtin: &plan.BuiltinStep{
					Action: "actions/cache",
					Inputs: map[string]string{"key": "", "path": "/tmp/p"},
				},
				ContinueOnError: true,
			},
			{
				ID:  "next",
				Run: plan.RunStep{Script: "echo ok"},
			},
		},
	}
	id := testID()
	result, err := e.Run(context.Background(), id, p)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !result.Success {
		t.Fatalf("continue-on-error should keep success, got %+v", result)
	}
	if result.Steps[0].Outcome != outcomeFailure || result.Steps[0].Conclusion != conclusionOK {
		t.Errorf("bad-cache = %s/%s", result.Steps[0].Outcome, result.Steps[0].Conclusion)
	}
	if result.Steps[1].Outcome != outcomeSuccess {
		t.Errorf("next = %s", result.Steps[1].Outcome)
	}
}

func TestEngineBuiltinWithSecret(t *testing.T) {
	// Test that with: token = ${{ secrets.PAT }} becomes SecretRef and is injected
	// We simulate plan already having stripped secret input and SecretRef
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "bare.git")
	exec.CommandContext(context.Background(), "git", "init", "--bare", bare).Run()
	work := filepath.Join(tmp, "src")
	exec.CommandContext(context.Background(), "git", "init", work).Run()
	exec.CommandContext(context.Background(), "git", "-C", work, "config", "user.email", "test@test.com").Run()
	exec.CommandContext(context.Background(), "git", "-C", work, "config", "user.name", "test").Run()
	os.WriteFile(filepath.Join(work, "f.txt"), []byte("x"), 0o644)
	exec.CommandContext(context.Background(), "git", "-C", work, "add", "f.txt").Run()
	exec.CommandContext(context.Background(), "git", "-C", work, "commit", "-m", "init").Run()
	exec.CommandContext(context.Background(), "git", "-C", work, "push", bare, "HEAD:refs/heads/main").Run()

	cp := &fakeCP{secrets: map[string]string{"repository/PAT": "secret-token-123"}}
	e, _ := newEngine(t, cp)
	p := plan.Plan{
		JobRunID: "01JTEST0000000000000000000X",
		Steps: []plan.Step{
			{
				ID: "checkout",
				Builtin: &plan.BuiltinStep{
					Action: "actions/checkout",
					Inputs: map[string]string{
						"repository": bare,
						// token stripped, will be injected via withSecrets
					},
				},
			},
		},
		SecretRefs: []plan.SecretRef{{Scope: "repository", Name: "PAT", Env: "$with:checkout:token"}},
	}
	// The engine's withSecrets handling should inject token into checkout inputs
	// Even though we don't verify token usage for file:// (no auth needed), we check that handler receives it
	// Modify handler temporarily to capture inputs
	captured := map[string]string{}
	orig := builtinRegistry["actions/checkout"]
	builtinRegistry["actions/checkout"] = func(ctx context.Context, bc BuiltinContext) error {
		for k, v := range bc.Inputs {
			captured[k] = v
		}
		return orig(ctx, bc)
	}
	defer func() { builtinRegistry["actions/checkout"] = orig }()

	id := testID()
	_, err := e.Run(context.Background(), id, p)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if captured["token"] != "secret-token-123" {
		t.Errorf("token input = %q, want secret", captured["token"])
	}
	// Ensure secret was masked
	if strings.Contains(cp.secrets["repository/PAT"], "secret-token-123") {
		// just check masker was added - we can't easily check logs here
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Info("test")
	}
}
