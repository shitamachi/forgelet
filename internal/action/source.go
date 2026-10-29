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
// These aliases are immutable, tested releases. Other public Actions require a full SHA.
var ActVersions = map[string]string{
	"actions/cache@v4":            "0057852bfaa89a56745cba8c7296529d2fc39830",
	"docker/login-action@v3":      "c94ce9fb468520275223c153574b00df6fe4bcc9",
	"docker/metadata-action@v5":   "c299e40c65443455700f0fdfc63efafe5b349051",
	"docker/build-push-action@v6": "10e90e3645eae34f1e60eeb005ba3a3d33f178e8",
	"actions/checkout@v4":         "11d5960a326750d5838078e36cf38b85af677262",
	"actions/setup-go@v5":         "40f1582b2485089dde7abd97c1529aa768e1baff",
	"codecov/codecov-action@v4":   "b9fd7d16f6d7d1b5d2bec1a2887e65ceed900238",
}

var shaPattern = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)
var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]*/[A-Za-z0-9_-][A-Za-z0-9_.-]*$`)

// RemoteReference parses syntax only. Resolving a mutable ref belongs to source
// acquisition, never the pure workflow compiler or a running job.
func RemoteReference(uses string) (*run.Action, error) {
	source, ref, ok := strings.Cut(uses, "@")
	parts := strings.SplitN(source, "/", 3)
	if !ok || len(uses) > 1024 || len(parts) < 2 || !validRef(ref) {
		return nil, fmt.Errorf("invalid public Action reference %q", uses)
	}
	a := &run.Action{Uses: uses, Repository: strings.ToLower(parts[0] + "/" + parts[1]), Ref: ref}
	if !repositoryPattern.MatchString(a.Repository) {
		return nil, fmt.Errorf("invalid Action repository")
	}
	if len(parts) == 3 {
		a.Path = parts[2]
		if a.Path == "" || !safePath(a.Path) {
			return nil, fmt.Errorf("invalid Action subdirectory")
		}
	}
	if shaPattern.MatchString(ref) {
		a.SHA = strings.ToLower(ref)
	}
	return a, nil
}
func validRef(ref string) bool {
	if ref == "" || len(ref) > 256 || ref == "@" || strings.HasSuffix(ref, ".") || strings.Contains(ref, "..") || strings.Contains(ref, "@{") || strings.ContainsAny(ref, " \\~^:?*[@") {
		return false
	}
	for _, r := range ref {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}
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
	a, err := RemoteReference(uses)
	if err != nil {
		return nil, err
	}
	// Retain existing native/legacy plan pins. New act plans resolve the original
	// Ref independently and persist their own lock, including these old aliases.
	if sha := ActVersions[uses]; sha != "" {
		a.SHA = sha
	}
	switch uses {
	case "actions/setup-go@v6.5.0":
		a.SHA = SetupGoSHA
	case "actions/setup-node@v6.5.0":
		a.SHA = SetupNodeSHA
	}
	return a, nil
}
func safePath(p string) bool {
	return !strings.ContainsAny(p, "\\\x00") && !path.IsAbs(p) && p != ".." && !strings.HasPrefix(path.Clean(p), "../") && path.Clean(p) == p || p == ""
}

// Download accepts an injected client for tests; production uses codeload.github.com
// and no repository credentials. Private remote actions are deliberately unsupported.
func Download(ctx context.Context, client *http.Client, base string, a run.Action, destination string) error {
	fixed, err := RemoteReference(a.Repository + "@" + a.SHA)
	if err != nil || fixed.SHA == "" {
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
	return extractArchive(ctx, resp.Body, destination)
}

func extractArchive(ctx context.Context, input io.Reader, destination string) error {
	compressed := &io.LimitedReader{R: input, N: 32 << 20}
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
