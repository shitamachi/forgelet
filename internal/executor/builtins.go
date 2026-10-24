package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/shitamachi/forgelet/internal/action"
	"github.com/shitamachi/forgelet/internal/blob"
	"github.com/shitamachi/forgelet/internal/run"
)

type Services interface {
	Upload(context.Context, string, string, string, int, string) (blob.Entry, error)
	Download(context.Context, string, string, string, []string, bool, string) (blob.Entry, error)
	Deploy(context.Context, string, string) (string, error)
}

func (s *actionSession) builtin(ctx context.Context, plan run.Plan, step run.Step, env, files map[string]string, sink *logSink, outputs map[string]map[string]string, records map[string]run.StepResult) (int, error) {
	uses := step.Action.Uses
	in := action.BuiltinInputs(uses)
	if err := action.ValidateBuiltin(uses, step.Action.Inputs); err != nil {
		return -1, err
	}
	for k, v := range in {
		if supplied, ok := step.Action.Inputs[k]; ok {
			v = supplied
		}
		expanded, err := interpolate(v, plan, env, outputs, records)
		if err != nil {
			return -1, err
		}
		in[k] = expanded
	}
	if uses == "forgelet/build@v1" {
		return s.build(ctx, plan, in, env, files, sink)
	}
	if s.opts.Services == nil {
		return -1, fmt.Errorf("%s requires control-plane services", uses)
	}
	if uses == "forgelet/deploy@v1" {
		id, err := s.opts.Services.Deploy(ctx, in["target"], in["digest"])
		if err != nil {
			return -1, err
		}
		return 0, builtinOutput(files, map[string]string{"deployment-id": id, "state": "queued"})
	}
	for _, key := range []string{"include-hidden-files", "lookup-only", "fail-on-cache-miss"} {
		if v, ok := in[key]; ok && v != "true" && v != "false" {
			return -1, fmt.Errorf("%s must be true or false", key)
		}
	}
	file, err := os.CreateTemp(s.meta, "archive-")
	if err != nil {
		return -1, err
	}
	archive := file.Name()
	if err := file.Close(); err != nil {
		return -1, err
	}
	defer func() { _ = os.Remove(archive) }()
	if uses == "actions/download-artifact@v4" {
		if _, err := s.opts.Services.Download(ctx, "artifact", in["name"], "", nil, false, archive); err != nil {
			return -1, err
		}
		if err := unpackFile(ctx, s.workspace, in["path"], archive); err != nil {
			return -1, err
		}
		return 0, builtinOutput(files, map[string]string{"download-path": filepath.Join(s.workspace, in["path"])})
	}
	paths, err := blob.Paths(in["path"])
	if err != nil {
		return -1, err
	}
	if uses == "actions/upload-artifact@v4" {
		days, err := strconv.Atoi(in["retention-days"])
		if err != nil || days < 1 || days > 90 {
			return -1, fmt.Errorf("retention-days must be 1..90")
		}
		policy := in["if-no-files-found"]
		if policy != "warn" && policy != "ignore" && policy != "error" {
			return -1, fmt.Errorf("invalid if-no-files-found")
		}
		err = packFile(ctx, s.workspace, paths, true, in["include-hidden-files"] == "true", archive)
		if errors.Is(err, blob.ErrNoFiles) && policy != "error" {
			if policy == "warn" {
				_, err = sink.Write([]byte("No files found for artifact\n"))
			} else {
				err = nil
			}
			return 0, err
		}
		if err != nil {
			return -1, err
		}
		e, err := s.opts.Services.Upload(ctx, "artifact", in["name"], "", days, archive)
		if err != nil {
			return -1, err
		}
		return 0, builtinOutput(files, map[string]string{"artifact-id": e.ID, "artifact-digest": e.Digest})
	}
	versionHash := sha256.Sum256([]byte("forgelet-tar-v1\x00" + runtime.GOOS + "\x00" + runtime.GOARCH + "\x00" + strings.Join(paths, "\x00")))
	version := hex.EncodeToString(versionHash[:])
	key := in["key"]
	if len(key) == 0 || len(key) > 512 || strings.ContainsAny(key, "\r\n\x00") {
		return -1, fmt.Errorf("invalid cache key")
	}
	save := func(ctx context.Context) error {
		f, err := os.CreateTemp(s.meta, "cache-")
		if err != nil {
			return err
		}
		name := f.Name()
		_ = f.Close()
		defer func() { _ = os.Remove(name) }()
		if err := packFile(ctx, s.workspace, paths, false, true, name); err != nil {
			return err
		}
		_, err = s.opts.Services.Upload(ctx, "cache", key, version, 7, name)
		return err
	}
	if uses == "actions/cache/save@v4" {
		return 0, save(ctx)
	}
	var prefixes []string
	for _, p := range strings.Split(in["restore-keys"], "\n") {
		if p = strings.TrimSpace(p); p != "" {
			prefixes = append(prefixes, p)
		}
	}
	e, err := s.opts.Services.Download(ctx, "cache", key, version, prefixes, in["lookup-only"] == "true", archive)
	if err != nil && !errors.Is(err, blob.ErrNotFound) {
		return -1, err
	}
	hit := ""
	matched := ""
	if err == nil {
		matched = e.Key
		hit = strconv.FormatBool(e.Key == key)
		if in["lookup-only"] != "true" {
			if err := unpackFile(ctx, s.workspace, ".", archive); err != nil {
				return -1, err
			}
		}
	} else if in["fail-on-cache-miss"] == "true" {
		return 1, nil
	}
	if uses == "actions/cache@v4" && hit != "true" && in["lookup-only"] != "true" {
		s.cacheSaves = append(s.cacheSaves, save)
	}
	return 0, builtinOutput(files, map[string]string{"cache-hit": hit, "cache-primary-key": key, "cache-matched-key": matched})
}
func packFile(ctx context.Context, workspace string, paths []string, strip, hidden bool, name string) error {
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	err = blob.Pack(ctx, workspace, paths, strip, hidden, f)
	return errors.Join(err, f.Close())
}
func unpackFile(ctx context.Context, workspace, path, name string) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return blob.Unpack(ctx, workspace, path, f)
}
func builtinOutput(files map[string]string, values map[string]string) error {
	f, err := os.OpenFile(files["OUTPUT"], os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	for _, k := range sortedMapKeys(values) {
		if strings.ContainsAny(values[k], "\r\n") {
			return fmt.Errorf("invalid builtin output")
		}
		if _, err := fmt.Fprintf(f, "%s=%s\n", k, values[k]); err != nil {
			return err
		}
	}
	return nil
}
