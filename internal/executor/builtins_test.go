package executor

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/blob"
	"github.com/shitamachi/forgelet/internal/run"
	"github.com/shitamachi/forgelet/internal/workflow"
)

type localServices struct {
	store      *blob.Store
	uploads    int
	deployment string
}

func (s *localServices) Upload(ctx context.Context, kind, key, version string, days int, filename string) (blob.Entry, error) {
	s.uploads++
	f, err := os.Open(filename)
	if err != nil {
		return blob.Entry{}, err
	}
	defer func() { _ = f.Close() }()
	return s.store.Put(ctx, blob.Scope{Repository: "o/r", Kind: kind, Namespace: "run", Version: version}, key, "job", time.Duration(days)*24*time.Hour, time.Now(), f)
}
func (s *localServices) Download(_ context.Context, kind, key, version string, prefixes []string, lookup bool, filename string) (blob.Entry, error) {
	e, f, err := s.store.Get(blob.Scope{Repository: "o/r", Kind: kind, Namespace: "run", Version: version}, key, prefixes, time.Now())
	if err != nil {
		return e, err
	}
	defer func() { _ = f.Close() }()
	if lookup {
		return e, nil
	}
	out, err := os.Create(filename)
	if err != nil {
		return e, err
	}
	_, err = io.Copy(out, f)
	return e, errors.Join(err, out.Close())
}
func (s *localServices) Deploy(_ context.Context, target, digest string) (string, error) {
	s.deployment = target + "@" + digest
	return "delivery", nil
}
func executeWorkflow(t *testing.T, source string, options Options) (run.Result, error) {
	t.Helper()
	plans, err := workflow.Compile([]byte("on: push\njobs:\n  job:\n    runs-on: test\n    steps:\n"+source), run.Event{Repository: "o/r", SHA: strings.Repeat("a", 40), Ref: "refs/heads/main"}, "run")
	if err != nil {
		t.Fatal(err)
	}
	return Execute(t.Context(), plans[0], options)
}

func TestArtifactAndCacheBuiltinRoundTrip(t *testing.T) {
	store, err := blob.Open(t.TempDir(), blob.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	services := &localServices{store: store}
	result, err := executeWorkflow(t, `      - run: mkdir -p dist cache; echo artifact > dist/result; echo cached > cache/file
      - id: uploaded
        uses: actions/upload-artifact@v4
        with: {name: build, path: dist}
      - uses: actions/download-artifact@v4
        with: {name: build, path: downloaded}
      - uses: actions/cache/save@v4
        with: {key: dependency-1, path: cache}
      - run: rm -rf cache; test "$(cat downloaded/result)" = artifact
      - id: cache
        uses: actions/cache/restore@v4
        with: {key: dependency-2, restore-keys: dependency-, path: cache}
      - run: test "$(cat cache/file)" = cached; test '${{ steps.cache.outputs.cache-hit }}' = false; test '${{ steps.uploaded.outputs.artifact-id }}' != ''
`, Options{Workspace: t.TempDir(), Services: services})
	if err != nil || result.State != run.Succeeded {
		t.Fatal(result, err)
	}
}
func TestCachePostSaveRequiresSuccessfulJob(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(string(rune('0'+map[bool]int{false: 0, true: 1}[fail])), func(t *testing.T) {
			store, err := blob.Open(t.TempDir(), blob.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			services := &localServices{store: store}
			source := `      - uses: actions/cache@v4
        with: {key: dependency, path: cache}
      - run: mkdir cache; echo content > cache/file
`
			if fail {
				source += "      - run: exit 9\n"
			}
			result, err := executeWorkflow(t, source, Options{Workspace: t.TempDir(), Services: services})
			if err != nil {
				t.Fatal(err)
			}
			if fail && (services.uploads != 0 || result.State != run.Failed) {
				t.Fatal(result, services.uploads)
			}
			if !fail && (services.uploads != 1 || result.State != run.Succeeded) {
				t.Fatal(result, services.uploads)
			}
		})
	}
}
func TestBuiltinUnsupportedOptionsAndFailures(t *testing.T) {
	store, err := blob.Open(t.TempDir(), blob.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	services := &localServices{store: store}
	for _, test := range []struct {
		name, source string
		fails        bool
	}{
		{"miss", "      - uses: actions/cache/restore@v4\n        with: {key: absent, path: cache, fail-on-cache-miss: true}\n", true},
		{"empty-warn", "      - uses: actions/upload-artifact@v4\n        with: {path: missing}\n", false},
		{"empty-error", "      - uses: actions/upload-artifact@v4\n        with: {path: missing, if-no-files-found: error}\n", true},
		{"glob", "      - uses: actions/upload-artifact@v4\n        with: {path: '*.txt'}\n", true},
		{"boolean", "      - uses: actions/upload-artifact@v4\n        with: {path: missing, include-hidden-files: yes}\n", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := executeWorkflow(t, test.source, Options{Workspace: t.TempDir(), Services: services})
			if test.fails && err == nil && result.State == run.Succeeded {
				t.Fatal("accepted invalid builtin")
			}
			if !test.fails && (err != nil || result.State != run.Succeeded) {
				t.Fatal(result, err)
			}
		})
	}
}
func TestBuildUsesExporterDigestAndConfiguredDestination(t *testing.T) {
	workspace := t.TempDir()
	writeActionFile(t, workspace, "Dockerfile", "FROM scratch\n")
	binary := filepath.Join(t.TempDir(), "buildctl")
	digest := "sha256:" + strings.Repeat("a", 64)
	script := "#!/bin/sh\nwhile [ $# -gt 0 ]; do if [ \"$1\" = --metadata-file ]; then shift; printf '%s' '{\"containerimage.digest\":\"" + digest + "\"}' > \"$1\"; fi; shift; done\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	source := "      - id: build\n        uses: forgelet/build@v1\n        with: {image: ghcr.io/test/app}\n      - run: test '${{ steps.build.outputs.digest }}' = '" + digest + "'\n"
	opts := Options{Workspace: workspace, Buildctl: binary, BuildAddress: "unix:///tmp/test.sock", BuildImages: []string{"ghcr.io/test/app"}}
	result, err := executeWorkflow(t, source, opts)
	if err != nil || result.State != run.Succeeded {
		t.Fatal(result, err)
	}
	opts.BuildImages = []string{"ghcr.io/test/other"}
	if _, err := executeWorkflow(t, source, opts); err == nil {
		t.Fatal("unregistered destination")
	}
}
