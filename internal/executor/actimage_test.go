package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreloadedActImageRequiresPinnedManifestAndExactConfig(t *testing.T) {
	config := "sha256:" + strings.Repeat("a", 64)
	manifest := `{"schemaVersion":2,"config":{"digest":"` + config + `"}}`
	sum := sha256.Sum256([]byte(manifest))
	ref := "registry.example/runner@sha256:" + hex.EncodeToString(sum[:])
	dir := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = buildx ]; then test -d "$BUILDX_CONFIG" || exit 2; printf '%s' "$ACT_TEST_MANIFEST"; else printf '%s' "$ACT_TEST_CONFIG"; fi
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, tc := range []struct {
		name, ref, manifest, local string
		want                       bool
	}{
		{"verified", ref, manifest, config, true},
		{"tag is not immutable", "registry.example/runner:tag", manifest, config, false},
		{"modified manifest", ref, manifest + " ", config, false},
		{"wrong local image", ref, manifest, "sha256:" + strings.Repeat("b", 64), false},
		{"missing image", ref, manifest, "", false},
		{"bounded metadata", ref, strings.Repeat("x", 65<<10), config, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ACT_TEST_MANIFEST", tc.manifest)
			t.Setenv("ACT_TEST_CONFIG", tc.local)
			got, ok := preloadedActImage(t.Context(), Options{ActImage: tc.ref})
			if ok != tc.want || ok && got != config {
				t.Fatal(got, ok)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, ok := preloadedActImage(ctx, Options{ActImage: ref}); ok {
		t.Fatal("accepted cancelled lookup")
	}
}
