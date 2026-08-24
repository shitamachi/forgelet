package executor

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// BuiltinContext carries the workspace and resolved inputs for one builtin step.
type BuiltinContext struct {
	Ctx       context.Context
	Workspace string
	Inputs    map[string]string
	Env       map[string]string
	Logger    *slog.Logger
	SetOutput func(key, value string)
}

// BuiltinHandler executes one builtin action. It must be idempotent for the
// same inputs and must not leak secrets into logs (masker already installed).
type BuiltinHandler func(ctx context.Context, bc BuiltinContext) error

// builtinRegistry maps canonical action names to handlers.
var builtinRegistry = map[string]BuiltinHandler{
	"actions/checkout": checkoutHandler,
}

func init() {
	builtinRegistry["actions/cache"] = notImplementedHandler("actions/cache")
	builtinRegistry["actions/upload-artifact"] = notImplementedHandler("actions/upload-artifact")
	builtinRegistry["actions/download-artifact"] = notImplementedHandler("actions/download-artifact")
}

func notImplementedHandler(name string) BuiltinHandler {
	return func(ctx context.Context, bc BuiltinContext) error {
		return fmt.Errorf("builtin %s not yet implemented", name)
	}
}

func checkoutHandler(ctx context.Context, bc BuiltinContext) error {
	repoInput := strings.TrimSpace(bc.Inputs["repository"])
	refInput := strings.TrimSpace(bc.Inputs["ref"])
	token := strings.TrimSpace(bc.Inputs["token"])
	fetchDepthStr := strings.TrimSpace(bc.Inputs["fetch-depth"])
	persistCreds := strings.TrimSpace(bc.Inputs["persist-credentials"])

	if persistCreds != "" && persistCreds != "true" && persistCreds != "false" {
		return fmt.Errorf("actions/checkout: persist-credentials must be true or false, got %q", persistCreds)
	}

	repoURL := repoInput
	if repoURL == "" {
		return fmt.Errorf("actions/checkout: repository input is required when no default is available")
	}
	// Normalize repository to URL
	if strings.HasPrefix(repoURL, "/") {
		repoURL = "file://" + repoURL
	} else if !strings.Contains(repoURL, "://") && strings.Count(repoURL, "/") == 1 {
		// owner/repo shorthand
		if !strings.HasSuffix(repoURL, ".git") {
			repoURL = "https://github.com/" + repoURL + ".git"
		} else {
			repoURL = "https://github.com/" + repoURL
		}
	}

	depth := 1
	if fetchDepthStr != "" {
		if fetchDepthStr == "0" {
			depth = 0
		} else {
			v, err := strconv.Atoi(fetchDepthStr)
			if err != nil || v < 0 {
				return fmt.Errorf("actions/checkout: invalid fetch-depth %q", fetchDepthStr)
			}
			depth = v
		}
	}

	ws := bc.Workspace
	if err := os.MkdirAll(ws, 0o755); err != nil {
		return fmt.Errorf("checkout: mkdir workspace: %w", err)
	}
	hasGit := false
	if _, err := os.Stat(filepath.Join(ws, ".git")); err == nil {
		hasGit = true
	}

	runGit := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = ws
		env := os.Environ()
		for k, v := range bc.Env {
			env = append(env, k+"="+v)
		}
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, string(out))
		}
		return nil
	}
	// Helper to add token auth via extraheader
	withToken := func(args []string) []string {
		if token == "" || !strings.HasPrefix(repoURL, "https://") {
			return args
		}
		host := "github.com"
		if strings.Contains(repoURL, "://") {
			parts := strings.SplitN(repoURL, "/", 4)
			if len(parts) >= 3 {
				host = parts[2]
			}
		}
		creds := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
		extra := fmt.Sprintf("http.https://%s/.extraheader=AUTHORIZATION: basic %s", host, creds)
		return append([]string{"-c", extra}, args...)
	}

	if !hasGit {
		if err := runGit("init"); err != nil {
			return err
		}
		if err := runGit("remote", "add", "origin", repoURL); err != nil {
			// remote may already exist
			_ = runGit("remote", "set-url", "origin", repoURL)
		}
	}
	// Fetch
	if refInput != "" {
		fetchArgs := []string{"fetch", "origin", refInput}
		if depth > 0 {
			fetchArgs = append(fetchArgs, "--depth", strconv.Itoa(depth))
		}
		fetchArgs = withToken(fetchArgs)
		if err := runGit(fetchArgs...); err != nil {
			return err
		}
		if err := runGit("checkout", "FETCH_HEAD"); err != nil {
			if err2 := runGit("checkout", refInput); err2 != nil {
				return fmt.Errorf("checkout %q: %v / %v", refInput, err, err2)
			}
		}
	} else {
		fetchArgs := []string{"fetch", "origin"}
		if depth > 0 {
			fetchArgs = append(fetchArgs, "--depth", strconv.Itoa(depth))
		}
		fetchArgs = withToken(fetchArgs)
		if err := runGit(fetchArgs...); err != nil {
			return err
		}
		// Checkout default branch (try main, then HEAD)
		if err := runGit("checkout", "origin/HEAD"); err != nil {
			_ = runGit("checkout", "main")
		}
		// Ensure files are checked out
		_ = runGit("checkout", "-f")
	}
	return nil
}
