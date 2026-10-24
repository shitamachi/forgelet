package blob

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

var ErrNoFiles = errors.New("no files matched")

// Paths deliberately accepts only literal workspace-relative paths. Unsupported
// glob/exclusion/home syntax is diagnosed rather than silently archived wrongly.
func Paths(value string) ([]string, error) {
	var paths []string
	for _, line := range strings.Split(value, "\n") {
		p := strings.TrimSpace(line)
		if p == "" {
			continue
		}
		p = strings.TrimSuffix(strings.TrimPrefix(p, "./"), "/")
		if p == "" {
			p = "."
		}
		if !SafePath(p) || strings.ContainsAny(p, "*?[]!~") {
			return nil, fmt.Errorf("archive paths must be literal workspace-relative paths")
		}
		paths = append(paths, p)
	}
	if len(paths) == 0 || len(paths) > 100 {
		return nil, fmt.Errorf("archive needs 1..100 paths")
	}
	slices.Sort(paths)
	return slices.Compact(paths), nil
}
func SafePath(p string) bool {
	return p != "" && !path.IsAbs(p) && path.Clean(p) == p && p != ".." && !strings.HasPrefix(p, "../") && !strings.ContainsAny(p, "\\\x00\r\n")
}

// Pack rejects links and special files, bounds the uncompressed payload, and
// uses os.Root so concurrent workspace edits cannot escape the root.
func Pack(ctx context.Context, workspace string, paths []string, stripBase, hidden bool, out io.Writer) error {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	base := "."
	if stripBase {
		for i, p := range paths {
			info, err := root.Stat(p)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			b := p
			if info == nil || !info.IsDir() {
				b = path.Dir(p)
			}
			if i == 0 {
				base = b
			} else {
				for base != "." && b != base && !strings.HasPrefix(b, base+"/") {
					base = path.Dir(base)
				}
			}
		}
	}
	tw := tar.NewWriter(out)
	seen := map[string]bool{}
	count := 0
	var total int64
	for _, p := range paths {
		if !SafePath(p) {
			return fmt.Errorf("invalid archive path")
		}
		err := fs.WalkDir(root.FS(), p, func(name string, entry fs.DirEntry, walkErr error) error {
			if errors.Is(walkErr, fs.ErrNotExist) {
				return nil
			}
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if !hidden && name != "." {
				for _, part := range strings.Split(name, "/") {
					if strings.HasPrefix(part, ".") {
						if entry.IsDir() {
							return fs.SkipDir
						}
						return nil
					}
				}
			}
			if entry.IsDir() {
				return nil
			}
			if seen[name] {
				return nil
			}
			seen[name] = true
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("archives do not support symlinks or special files")
			}
			count++
			total += info.Size()
			if count > 10000 || total > MaxArchive {
				return ErrLimit
			}
			rel := name
			if base != "." {
				rel = strings.TrimPrefix(rel, base+"/")
			}
			if !SafePath(rel) {
				return fmt.Errorf("invalid archive path")
			}
			f, err := root.Open(name)
			if err != nil {
				return err
			}
			header := &tar.Header{Name: rel, Mode: int64(info.Mode().Perm() & 0777), Size: info.Size(), Typeflag: tar.TypeReg, Format: tar.FormatPAX}
			err = tw.WriteHeader(header)
			if err == nil {
				_, err = io.CopyN(tw, &contextReader{ctx: ctx, r: f}, info.Size())
			}
			return errors.Join(err, f.Close())
		})
		if err != nil {
			return err
		}
	}
	if count == 0 {
		return ErrNoFiles
	}
	return tw.Close()
}

// Unpack validates into a temporary directory before changing the workspace.
// File publication is atomic, never follows an existing destination symlink,
// and never accepts archive links. It is not an atomic directory replacement.
func Unpack(ctx context.Context, workspace, destination string, input io.Reader) error {
	if !SafePath(destination) {
		return fmt.Errorf("invalid archive destination")
	}
	staging, err := os.MkdirTemp("", "forgelet-unpack-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	tr := tar.NewReader(io.LimitReader(&contextReader{ctx: ctx, r: input}, MaxArchive+1))
	var names []string
	seen := map[string]bool{}
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		if !SafePath(h.Name) || h.Name == "." || seen[h.Name] || h.Typeflag != tar.TypeReg || h.Size < 0 {
			return fmt.Errorf("archive contains unsafe or duplicate entry")
		}
		total += h.Size
		if total > MaxArchive || len(names) >= 10000 {
			return ErrLimit
		}
		seen[h.Name] = true
		names = append(names, h.Name)
		target := filepath.Join(staging, filepath.FromSlash(h.Name))
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(h.Mode)&0777)
		if err != nil {
			return err
		}
		_, err = io.CopyN(f, tr, h.Size)
		if err = errors.Join(err, f.Close()); err != nil {
			return err
		}
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		p := path.Join(destination, name)
		if err := root.MkdirAll(path.Dir(p), 0700); err != nil {
			return err
		}
		// Root confines parent symlinks; reject the final symlink as well.
		if info, err := root.Lstat(p); err == nil && !info.Mode().IsRegular() {
			return fmt.Errorf("archive destination is not a regular file")
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		in, err := os.Open(filepath.Join(staging, filepath.FromSlash(name)))
		if err != nil {
			return err
		}
		info, err := in.Stat()
		if err != nil {
			_ = in.Close()
			return err
		}
		// Random names inside the destination parent avoid a cross-filesystem rename.
		tmp := path.Join(path.Dir(p), ".forgelet-"+filepath.Base(staging)+"-"+fmt.Sprint(len(p)))
		out, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			_ = in.Close()
			return err
		}
		_, err = io.Copy(out, in)
		err = errors.Join(err, out.Close(), in.Close())
		if err == nil {
			err = root.Rename(tmp, p)
		}
		if err != nil {
			_ = root.Remove(tmp)
			return err
		}
	}
	return nil
}
