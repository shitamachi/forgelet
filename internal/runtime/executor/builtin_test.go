package executor

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckoutClonesFile(t *testing.T) {
	// Create a bare repo with one commit
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "bare.git")
	if out, err := exec.Command("git", "init", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v %s", err, out)
	}
	work := filepath.Join(tmp, "src")
	if out, err := exec.Command("git", "init", work).CombinedOutput(); err != nil {
		t.Fatalf("init work: %v %s", err, out)
	}
	// Configure git in work
	if out, err := exec.Command("git", "-C", work, "config", "user.email", "test@test.com").CombinedOutput(); err != nil {
		t.Fatalf("config email: %v %s", err, out)
	}
	if out, err := exec.Command("git", "-C", work, "config", "user.name", "test").CombinedOutput(); err != nil {
		t.Fatalf("config name: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(work, "hello.txt"), []byte("hello checkout"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", work, "add", "hello.txt").CombinedOutput(); err != nil {
		t.Fatalf("add: %v %s", err, out)
	}
	if out, err := exec.Command("git", "-C", work, "commit", "-m", "init").CombinedOutput(); err != nil {
		t.Fatalf("commit: %v %s", err, out)
	}
	if out, err := exec.Command("git", "-C", work, "push", bare, "HEAD:refs/heads/main").CombinedOutput(); err != nil {
		t.Fatalf("push: %v %s", err, out)
	}
	// Get SHA
	shaOut, _ := exec.Command("git", "-C", work, "rev-parse", "HEAD").Output()
	sha := strings.TrimSpace(string(shaOut))

	ws := t.TempDir()
	bc := BuiltinContext{
		Ctx:       context.Background(),
		Workspace: ws,
		Inputs: map[string]string{
			"repository": bare,
			"ref":        sha,
		},
		Env:    map[string]string{},
		Logger: slog.New(slog.NewJSONHandler(os.Stderr, nil)),
		SetOutput: func(k, v string) {},
	}
	if err := checkoutHandler(context.Background(), bc); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(ws, "hello.txt"))
	if err != nil {
		t.Fatalf("read hello.txt: %v", err)
	}
	if string(data) != "hello checkout" {
		t.Errorf("hello.txt = %q", string(data))
	}
	// Check fetch-depth handling: depth 1 should still have file
	ws2 := t.TempDir()
	bc2 := BuiltinContext{
		Ctx:       context.Background(),
		Workspace: ws2,
		Inputs: map[string]string{
			"repository":  bare,
			"ref":         sha,
			"fetch-depth": "1",
		},
		Env:       map[string]string{},
		Logger:    slog.New(slog.NewJSONHandler(os.Stderr, nil)),
		SetOutput: func(k, v string) {},
	}
	if err := checkoutHandler(context.Background(), bc2); err != nil {
		t.Fatalf("checkout depth 1: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws2, "hello.txt")); err != nil {
		t.Errorf("depth 1 checkout missing file: %v", err)
	}
}

func TestCheckoutInputValidation(t *testing.T) {
	bc := BuiltinContext{
		Ctx:       context.Background(),
		Workspace: t.TempDir(),
		Inputs: map[string]string{
			"fetch-depth": "-1",
		},
		Env:    map[string]string{},
		Logger: slog.New(slog.NewJSONHandler(os.Stderr, nil)),
	}
	// Need repository
	bc.Inputs = map[string]string{"fetch-depth": "-1", "repository": "/tmp/x"}
	if err := checkoutHandler(context.Background(), bc); err == nil || !strings.Contains(err.Error(), "fetch-depth") {
		t.Errorf("expected fetch-depth error, got %v", err)
	}
	bc2 := BuiltinContext{
		Ctx:       context.Background(),
		Workspace: t.TempDir(),
		Inputs: map[string]string{
			"repository":         "/tmp/x",
			"persist-credentials": "maybe",
		},
		Env:    map[string]string{},
		Logger: slog.New(slog.NewJSONHandler(os.Stderr, nil)),
	}
	if err := checkoutHandler(context.Background(), bc2); err == nil || !strings.Contains(err.Error(), "persist-credentials") {
		t.Errorf("expected persist-credentials error, got %v", err)
	}
}

func TestBuiltinNotImplemented(t *testing.T) {
	h := builtinRegistry["actions/cache"]
	err := h(context.Background(), BuiltinContext{})
	if err == nil || !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("expected not implemented, got %v", err)
	}
}
