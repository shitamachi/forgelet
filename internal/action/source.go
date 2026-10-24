// Package action resolves immutable public Action references and extracts bounded
// source archives. It has no Kubernetes, database or runner-service dependency.
package action

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/shitamachi/forgelet/internal/run"
)

const SetupGoSHA = "924ae3a1cded613372ab5595356fb5720e22ba16"   // v6.5.0
const SetupNodeSHA = "249970729cb0ef3589644e2896645e5dc5ba9c38" // v6.5.0
var refPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_./-]+)?@[a-fA-F0-9]{40}$`)

func Reference(uses string) (*run.Action, error) {
	a := &run.Action{Uses: uses}
	if BuiltinInputs(uses) != nil {
		return a, nil
	}
	if strings.HasPrefix(uses, "./") {
		a.Path = strings.TrimPrefix(uses, "./")
		if !safePath(a.Path) {
			return nil, fmt.Errorf("local action path must stay in workspace")
		}
		return a, nil
	}
	pinned := uses
	switch uses {
	case "actions/setup-go@v6.5.0":
		pinned = "actions/setup-go@" + SetupGoSHA
	case "actions/setup-node@v6.5.0":
		pinned = "actions/setup-node@" + SetupNodeSHA
	}
	if !refPattern.MatchString(pinned) {
		return nil, fmt.Errorf("unsupported action %q: remote actions require a full commit SHA or a documented pinned version", uses)
	}
	repo, sha, _ := strings.Cut(pinned, "@")
	parts := strings.SplitN(repo, "/", 3)
	a.Repository = parts[0] + "/" + parts[1]
	a.SHA = strings.ToLower(sha)
	if len(parts) == 3 {
		a.Path = parts[2]
	}
	if !safePath(repo) || !safePath(a.Path) {
		return nil, fmt.Errorf("invalid action repository or path")
	}
	return a, nil
}
func safePath(p string) bool {
	return !strings.ContainsAny(p, "\\\x00") && !path.IsAbs(p) && p != ".." && !strings.HasPrefix(path.Clean(p), "../") && path.Clean(p) == p || p == ""
}

// Download accepts an injected client for tests; production uses codeload.github.com
// and no repository credentials. Private remote actions are deliberately unsupported.
func Download(ctx context.Context, client *http.Client, base string, a run.Action, destination string) error {
	if a.Repository == "" || len(a.SHA) != 40 {
		return fmt.Errorf("action requires immutable source")
	}
	if base == "" {
		base = "https://codeload.github.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/"+a.Repository+"/tar.gz/"+a.SHA, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download action source: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download action source: HTTP %d", resp.StatusCode)
	}
	compressed := &io.LimitedReader{R: resp.Body, N: 32 << 20}
	gz, err := gzip.NewReader(compressed)
	if err != nil {
		return fmt.Errorf("open action archive: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	var total int64
	count := 0
	prefix := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read action archive: %w", err)
		}
		count++
		total += header.Size
		if count > 10000 || header.Size < 0 || total > 128<<20 {
			return fmt.Errorf("action archive exceeds size or file count limit")
		}
		// GitHub codeload includes a global PAX record containing the commit.
		// archive/tar has already parsed it; it is metadata, not a filesystem entry.
		if header.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		if path.IsAbs(header.Name) {
			return fmt.Errorf("action archive contains absolute path")
		}
		top, relative, ok := strings.Cut(header.Name, "/")
		if !ok {
			if header.Typeflag != tar.TypeDir || !safePath(top) {
				return fmt.Errorf("action archive requires a root directory")
			}
		}
		if prefix == "" {
			prefix = top
		}
		relative = strings.TrimSuffix(relative, "/")
		if top != prefix || !safePath(relative) {
			return fmt.Errorf("action archive contains unsafe path")
		}
		if relative == "" {
			continue
		}
		target := filepath.Join(destination, filepath.FromSlash(relative))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return err
			}
			mode := os.FileMode(0600)
			if header.Mode&0111 != 0 {
				mode = 0700
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(f, tr, header.Size)
			closeErr := f.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("action archive links and special files are unsupported")
		}
	}
	if compressed.N == 0 {
		return fmt.Errorf("compressed action exceeds 32 MiB")
	}
	return nil
}
