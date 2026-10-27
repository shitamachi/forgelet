package run

import (
	"strings"
	"testing"
)

func TestImageMirrorReferences(t *testing.T) {
	digest := "@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name   string
		images map[string]string
		bad    bool
	}{
		{"tag alias", map[string]string{"postgres:18-alpine": "zot.example/mirrors/postgres" + digest}, false},
		{"normalized duplicate", map[string]string{"postgres:18-alpine": "zot.example/a" + digest, "docker.io/library/postgres:18-alpine": "zot.example/a" + digest}, true},
		{"mutable source", map[string]string{"postgres:18-alpine": "zot.example/mirrors/postgres:18-alpine"}, true},
		{"different credential host", map[string]string{"postgres:18-alpine": "other.example/postgres" + digest}, true},
		{"digest alias is not a tag", map[string]string{"postgres" + digest: "zot.example/postgres" + digest}, true},
		{"private alias", map[string]string{"private.example/postgres:18": "zot.example/postgres" + digest}, true},
		{"missing tag", map[string]string{"postgres": "zot.example/postgres" + digest}, true},
		{"shell injection", map[string]string{"postgres:18;touch /tmp/bad": "zot.example/postgres" + digest}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateImageMirrors("zot.example/runner"+digest, tc.images)
			if (err != nil) != tc.bad {
				t.Fatal(err)
			}
		})
	}
	tooMany := map[string]string{}
	for i := range 17 {
		tooMany[strings.Repeat("a", i+1)+":1"] = "zot.example/a" + digest
	}
	if ValidateImageMirrors("zot.example/a"+digest, tooMany) == nil {
		t.Fatal("accepted unbounded catalog")
	}
}
