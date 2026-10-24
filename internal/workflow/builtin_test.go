package workflow

import (
	"strings"
	"testing"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestBuiltinInputsAreExplicit(t *testing.T) {
	for _, tc := range []struct {
		uses, inputs string
		valid        bool
	}{
		{"actions/upload-artifact@v4", "{path: dist}", true}, {"actions/download-artifact@v4", "{name: dist}", true}, {"actions/cache@v4", "{key: deps, path: cache}", true}, {"forgelet/build@v1", "{image: ghcr.io/test/app}", true}, {"forgelet/deploy@v1", "{target: app, digest: '${{ steps.a.outputs.digest }}'}", true},
		{"actions/upload-artifact@v3", "{path: dist}", false}, {"actions/upload-artifact@v4", "{path: dist, overwrite: true}", false}, {"actions/download-artifact@v4", "{run-id: 12}", false}, {"actions/cache@v4", "{key: deps, path: cache, enableCrossOsArchive: true}", false}, {"forgelet/build@v1", "{image: ghcr.io/test/app, platforms: linux/amd64}", false}, {"forgelet/deploy@v1", "{target: app}", false},
	} {
		t.Run(tc.uses+tc.inputs, func(t *testing.T) {
			_, err := Compile([]byte("on: push\njobs:\n  test:\n    runs-on: test\n    steps:\n      - id: a\n        run: echo test\n      - uses: "+tc.uses+"\n        with: "+tc.inputs+"\n"), run.Event{Repository: "test/project", SHA: strings.Repeat("a", 40), Ref: "refs/heads/main"}, "run")
			if (err == nil) != tc.valid {
				t.Fatal(tc, err)
			}
		})
	}
}
