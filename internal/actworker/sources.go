package actworker

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nektos/act/pkg/common"

	"github.com/shitamachi/forgelet/internal/action"
	"github.com/shitamachi/forgelet/internal/run"
)

type sources struct {
	dir, base string
	preloaded string
	pins      map[string]string
	mu        sync.Mutex
}

func (s *sources) Fetch(ctx context.Context, key, url, ref, token string) (string, error) {
	if url != "https://github.com/"+key {
		return "", fmt.Errorf("only public GitHub Action sources are supported")
	}
	key = strings.ToLower(key)
	sha := s.pins[key+"@"+ref]
	if sha == "" && len(s.pins) == 0 {
		sha = action.ActVersions[key+"@"+ref]
	}
	if sha == "" {
		a, err := action.Reference(key + "@" + ref)
		if err != nil || a.SHA == "" || (len(s.pins) > 0 && !strings.EqualFold(ref, a.SHA)) {
			return "", fmt.Errorf("action requires an immutable validated reference: %s@%s", key, ref)
		}
		sha = a.SHA
	}
	common.Logger(ctx).Infof("Action source %s@%s", key, sha)
	if _, err := s.ensure(ctx, key, sha); err != nil {
		return "", err
	}
	return sha, nil
}
func (s *sources) GetTarArchive(ctx context.Context, key, sha, prefix string) (io.ReadCloser, error) {
	a, err := action.Reference(key + "@" + sha)
	if err != nil || a.SHA == "" {
		return nil, fmt.Errorf("invalid Action archive reference")
	}
	if prefix == "" {
		prefix = "."
	}
	if path.Clean(prefix) != prefix || path.IsAbs(prefix) || prefix == ".." || strings.HasPrefix(prefix, "../") {
		return nil, fmt.Errorf("invalid Action source prefix")
	}
	dir, err := s.ensure(ctx, key, sha)
	if err != nil {
		return nil, err
	}
	root := dir
	if prefix != "." {
		root = filepath.Join(dir, prefix)
	}
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	err = filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("action contains a special file")
		}
		relative, err := filepath.Rel(root, name)
		if relative == "." {
			relative = filepath.Base(name)
		}
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		file, err := os.Open(name)
		if err != nil {
			return err
		}
		_, err = io.Copy(writer, file)
		return errors.Join(err, file.Close())
	})
	if err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(buffer.Bytes())), nil
}

func (s *sources) ensure(ctx context.Context, key, sha string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Join(s.dir, filepath.FromSlash(key), sha)
	if _, err := os.Stat(dir); err == nil {
		return dir, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return "", err
	}
	temporary, err := os.MkdirTemp(filepath.Dir(dir), ".preloaded-")
	if err != nil {
		return "", err
	}
	found, loadErr := action.LoadPreloaded(ctx, s.preloaded, run.Action{Repository: key, SHA: sha}, temporary)
	if loadErr == nil && found {
		loadErr = os.Rename(temporary, dir)
	}
	if !found || loadErr != nil {
		_ = os.RemoveAll(temporary)
	}
	if loadErr != nil {
		return "", fmt.Errorf("preloaded immutable Action %s@%s: %w", key, sha, loadErr)
	}
	if found {
		common.Logger(ctx).Infof("Using verified preloaded Action %s@%s", key, sha)
		return dir, nil
	}
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		temporary, err := os.MkdirTemp(filepath.Dir(dir), ".download-")
		if err != nil {
			return "", err
		}
		last = action.Download(ctx, &http.Client{Timeout: 2 * time.Minute}, s.base, run.Action{Repository: key, SHA: sha}, temporary)
		if last == nil {
			last = os.Rename(temporary, dir)
		}
		if last == nil {
			return dir, nil
		}
		_ = os.RemoveAll(temporary)
		common.Logger(ctx).Warnf("Action source %s@%s download attempt %d failed: %v", key, sha, attempt+1, last)
		if attempt < 2 {
			timer := time.NewTimer(time.Duration(attempt+1) * 100 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return "", ctx.Err()
			case <-timer.C:
			}
		}
	}
	return "", fmt.Errorf("download immutable Action %s@%s: %w", key, sha, last)
}
