//nolint:errcheck
package executor

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/shitamachi/forgelet/internal/security/identity"
)

// BuiltinContext carries the workspace and resolved inputs for one builtin step.
type BuiltinContext struct {
	Ctx       context.Context
	Workspace string
	Inputs    map[string]string
	Env       map[string]string
	Logger    *slog.Logger
	SetOutput func(key, value string)
	CP        ControlPlane
	Identity  identity.Identity
}

// BuiltinHandler executes one builtin action. It must be idempotent for the
// same inputs and must not leak secrets into logs (masker already installed).
type BuiltinHandler func(ctx context.Context, bc BuiltinContext) error

// builtinRegistry maps canonical action names to handlers.
var builtinRegistry = map[string]BuiltinHandler{
	"actions/checkout":          checkoutHandler,
	"actions/cache":             cacheHandler,
	"actions/upload-artifact":   uploadArtifactHandler,
	"actions/download-artifact": downloadArtifactHandler,
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
				return fmt.Errorf("checkout %q: %w", refInput, errors.Join(err, err2))
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

func cacheHandler(ctx context.Context, bc BuiltinContext) error {
	key := strings.TrimSpace(bc.Inputs["key"])
	pathStr := strings.TrimSpace(bc.Inputs["path"])
	if key == "" {
		return fmt.Errorf("actions/cache: key is required")
	}
	if pathStr == "" {
		return fmt.Errorf("actions/cache: path is required")
	}
	restoreKeysRaw := bc.Inputs["restore-keys"]
	var restoreKeys []string
	if restoreKeysRaw != "" {
		// split on newline and comma
		for _, line := range strings.Split(restoreKeysRaw, "\n") {
			for _, part := range strings.Split(line, ",") {
				p := strings.TrimSpace(part)
				if p != "" {
					restoreKeys = append(restoreKeys, p)
				}
			}
		}
	}
	if bc.CP == nil {
		return fmt.Errorf("actions/cache: control plane not configured")
	}
	hit, getURL, putURL, err := bc.CP.ResolveCache(ctx, bc.Identity, key, restoreKeys)
	if err != nil {
		return fmt.Errorf("cache resolve: %w", err)
	}
	cachePath := pathStr
	if !filepath.IsAbs(cachePath) {
		cachePath = filepath.Join(bc.Workspace, cachePath)
	}
	if hit && getURL != "" {
		bc.Logger.Info("cache hit, restoring", "key", key, "path", pathStr)
		if err := downloadAndExtract(ctx, getURL, cachePath); err != nil {
			bc.Logger.Warn("cache restore failed, continuing", "err", err.Error())
		} else {
			bc.Logger.Info("cache restored", "key", key)
		}
		if bc.SetOutput != nil {
			bc.SetOutput("cache-hit", "true")
		}
		return nil
	}
	if bc.SetOutput != nil {
		bc.SetOutput("cache-hit", "false")
	}
	bc.Logger.Info("cache miss, will save", "key", key)
	// Best-effort save: tar current path and upload. If path doesn't exist, skip.
	if _, err := os.Stat(cachePath); err != nil {
		bc.Logger.Warn("cache path not found, skipping save", "path", pathStr)
		return nil //nolint:nilerr
	}
	if err := tarAndUpload(ctx, cachePath, putURL); err != nil {
		bc.Logger.Warn("cache save failed", "err", err.Error())
	}
	return nil
}

func uploadArtifactHandler(ctx context.Context, bc BuiltinContext) error {
	name := strings.TrimSpace(bc.Inputs["name"])
	pathStr := strings.TrimSpace(bc.Inputs["path"])
	if name == "" {
		return fmt.Errorf("actions/upload-artifact: name is required")
	}
	if pathStr == "" {
		return fmt.Errorf("actions/upload-artifact: path is required")
	}
	if bc.CP == nil {
		return fmt.Errorf("upload-artifact: control plane not configured")
	}
	uploadURL, err := bc.CP.ArtifactUploadURL(ctx, bc.Identity, name)
	if err != nil {
		return fmt.Errorf("artifact upload URL: %w", err)
	}
	// Resolve path (may be glob)
	absPath := pathStr
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(bc.Workspace, absPath)
	}
	// If path contains wildcard, expand
	var sources []string
	if strings.Contains(absPath, "*") || strings.Contains(absPath, "?") || strings.Contains(absPath, "[") {
		matches, err := filepath.Glob(absPath)
		if err != nil {
			return fmt.Errorf("glob %q: %w", pathStr, err)
		}
		sources = matches
		if len(sources) == 0 {
			bc.Logger.Warn("upload-artifact: no files matched", "path", pathStr)
			return nil
		}
	} else {
		sources = []string{absPath}
	}
	if err := tarAndUploadMultiple(ctx, sources, bc.Workspace, uploadURL); err != nil {
		return fmt.Errorf("upload artifact: %w", err)
	}
	bc.Logger.Info("artifact uploaded", "name", name)
	return nil
}

func downloadArtifactHandler(ctx context.Context, bc BuiltinContext) error {
	name := strings.TrimSpace(bc.Inputs["name"])
	pathStr := strings.TrimSpace(bc.Inputs["path"])
	if name == "" {
		return fmt.Errorf("actions/download-artifact: name is required")
	}
	if bc.CP == nil {
		return fmt.Errorf("download-artifact: control plane not configured")
	}
	downloadURL, err := bc.CP.ArtifactDownloadURL(ctx, bc.Identity, name)
	if err != nil {
		return fmt.Errorf("artifact download URL: %w", err)
	}
	dest := bc.Workspace
	if pathStr != "" {
		dest = pathStr
		if !filepath.IsAbs(dest) {
			dest = filepath.Join(bc.Workspace, dest)
		}
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fmt.Errorf("mkdir dest: %w", err)
	}
	if err := downloadAndExtract(ctx, downloadURL, dest); err != nil {
		return fmt.Errorf("download artifact: %w", err)
	}
	bc.Logger.Info("artifact downloaded", "name", name, "dest", dest)
	return nil
}

func tarAndUpload(ctx context.Context, srcPath, putURL string) error {
	// Tar srcPath (file or dir) into gzip and PUT
	tmp, err := os.CreateTemp("", "forgelet-cache-*.tar.gz")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck
	defer tmp.Close()        //nolint:errcheck
	gw := gzip.NewWriter(tmp)
	tw := tar.NewWriter(gw)
	err = filepath.Walk(srcPath, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, _ := filepath.Rel(filepath.Dir(srcPath), path)
		// For dir source, we want to include top-level dir name
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = rel
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, f)
		_ = f.Close() //nolint:errcheck
		return err
	})
	if err != nil {
		_ = tw.Close() //nolint:errcheck
		_ = gw.Close() //nolint:errcheck
		return err
	}
	_ = tw.Close()  //nolint:errcheck
	_ = gw.Close()  //nolint:errcheck
	_ = tmp.Close() //nolint:errcheck
	f, err := os.Open(tmpName)
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck
	info, _ := f.Stat()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, putURL, f)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = info.Size()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()               //nolint:errcheck
	_, _ = io.Copy(io.Discard, resp.Body) //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("upload status %d", resp.StatusCode)
	}
	return nil
}

func tarAndUploadMultiple(ctx context.Context, sources []string, workspace, putURL string) error {
	tmp, err := os.CreateTemp("", "forgelet-artifact-*.tar.gz")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck
	defer tmp.Close()        //nolint:errcheck
	gw := gzip.NewWriter(tmp)
	tw := tar.NewWriter(gw)
	for _, src := range sources {
		err = filepath.Walk(src, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, _ := filepath.Rel(workspace, path)
			if rel == "." {
				return nil
			}
			hdr, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			hdr.Name = rel
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, f)
			_ = f.Close() //nolint:errcheck
			return err
		})
		if err != nil {
			_ = tw.Close() //nolint:errcheck
			_ = gw.Close() //nolint:errcheck
			return err
		}
	}
	_ = tw.Close()  //nolint:errcheck
	_ = gw.Close()  //nolint:errcheck
	_ = tmp.Close() //nolint:errcheck
	f, err := os.Open(tmpName)
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck
	info, _ := f.Stat()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, putURL, f)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = info.Size()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()               //nolint:errcheck
	_, _ = io.Copy(io.Discard, resp.Body) //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("upload status %d", resp.StatusCode)
	}
	return nil
}

func downloadAndExtract(ctx context.Context, getURL, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, getURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download status %d", resp.StatusCode)
	}
	gr, err := gzip.NewReader(resp.Body)
	if err != nil {
		return err
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dest, hdr.Name)
		// Prevent directory traversal
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dest)) {
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				_ = f.Close() //nolint:errcheck
				return err
			}
			_ = f.Close() //nolint:errcheck
		}
	}
	return nil
}
