package executor

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMirrorPreparationUsesSupervisorCredentialAndExactAliasIdentity(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$MIRROR_CALLS"
case "$1" in
pull) test "$DOCKER_CONFIG" = /supervisor-only || exit 9; test "$MIRROR_FAIL" != pull || exit 5;;
tag) test "$MIRROR_FAIL" != tag || exit 6;;
image) printf 'sha256:%064d\n' 1; if [ "$MIRROR_FAIL" = identity ]; then printf 'sha256:%064d\n' 2; else printf 'sha256:%064d\n' 1; fi;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MIRROR_CALLS", filepath.Join(dir, "calls"))
	for _, mode := range []string{"", "pull", "tag", "identity"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("MIRROR_FAIL", mode)
			var logs bytes.Buffer
			sink := &logSink{mask: newMasker(), emit: func(_ string, b []byte) error { _, err := logs.Write(b); return err }}
			err := prepareActMirrors(t.Context(), Options{RegistryConfig: "/supervisor-only", ImageMirrors: map[string]string{"postgres:18-alpine": "zot.example/postgres@sha256:" + strings.Repeat("a", 64)}}, sink)
			if (err != nil) != (mode != "") {
				t.Fatal(err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := prepareActMirrors(ctx, Options{ImageMirrors: map[string]string{"a:1": "zot.example/a"}}, &logSink{mask: newMasker()}); err == nil {
		t.Fatal("ignored cancellation")
	}
	data, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "/supervisor-only") {
		t.Fatal("credential path passed as an argument")
	}
}
