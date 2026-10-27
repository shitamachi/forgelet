package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/shitamachi/forgelet/internal/kube"
	"github.com/shitamachi/forgelet/internal/run"
	"github.com/shitamachi/forgelet/internal/storage"
)

func TestPackageCredentialRequiresExplicitBindingAndKeepsCloneReadOnly(t *testing.T) {
	h := newHarness(t, 1)
	h.http.Close()
	h.cfg.Profiles["actions"] = kube.Profile{Image: "executor:act", Act: &kube.ActProfile{}}
	h.cfg.PackageTokens = map[string]string{"me/repo": "GHCR_TOKEN"}
	h.cfg.SecretKey = bytes.Repeat([]byte{7}, 32)
	h.start()
	if code, _ := h.admin(http.MethodPut, "/api/repositories/me/repo/secrets/GHCR_TOKEN", map[string]string{"value": "independent-package-publish-token"}); code != 200 {
		t.Fatal(code)
	}
	item := h.manual(`on: push
permissions: {contents: read, packages: write}
jobs:
  publish:
    runs-on: actions
    steps:
      - uses: actions/checkout@v4
      - run: echo '${{ secrets.GITHUB_TOKEN }}'
`)
	p := item.Jobs[0].Plan
	if p.PackageTokenSecret != "GHCR_TOKEN" {
		t.Fatal(p)
	}
	encoded, _ := json.Marshal(p)
	if bytes.Contains(encoded, []byte("independent-package-publish-token")) {
		t.Fatal("persisted credential value")
	}
	h.tick()
	job := item.Jobs[0]
	if code, _ := h.runtimeRequest(http.MethodPost, "claim", job, nil); code != 200 {
		t.Fatal(code)
	}
	code, body := h.runtimeRequest(http.MethodGet, "credentials", job, nil)
	if code != 200 || !bytes.Contains(body, []byte(`"package_token":"independent-package-publish-token"`)) || !bytes.Contains(body, []byte(`"clone_token":"repository-scoped-clone-token"`)) {
		t.Fatal(code, string(body))
	}
	// Restart preserves only the reference, and reads the current encrypted value.
	reopen := func() {
		h.http.Close()
		if err := h.store.Close(); err != nil {
			t.Fatal(err)
		}
		var err error
		h.store, err = storage.Open(t.Context(), h.path)
		if err != nil {
			t.Fatal(err)
		}
		h.start()
	}
	reopen()
	if code, _ := h.admin(http.MethodPut, "/api/repositories/me/repo/secrets/GHCR_TOKEN", map[string]string{"value": "rotated-package-token"}); code != 200 {
		t.Fatal(code)
	}
	code, body = h.runtimeRequest(http.MethodGet, "credentials", job, nil)
	if code != 200 || !bytes.Contains(body, []byte("rotated-package-token")) {
		t.Fatal(code, string(body))
	}
	h.cfg.PackageTokens = nil
	reopen()
	if code, _ := h.runtimeRequest(http.MethodGet, "credentials", job, nil); code != 503 {
		t.Fatal("revoked binding still issued credential", code)
	}
}
func TestPackageAdmissionRejectsMissingBindingNativeAndPR(t *testing.T) {
	for _, tc := range []struct {
		name, profile, event string
		bindings             map[string]string
		want                 string
	}{
		{"unconfigured", "actions", "push", nil, "package_tokens"},
		{"native", "small", "push", map[string]string{"me/repo": "GHCR_TOKEN"}, "act profile"},
		{"pull request", "actions", "pull_request", map[string]string{"me/repo": "GHCR_TOKEN"}, "PR publication"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, 1)
			h.http.Close()
			h.cfg.Profiles["actions"] = kube.Profile{Image: "executor:act", Act: &kube.ActProfile{}}
			h.cfg.PackageTokens = tc.bindings
			h.start()
			plans := []run.Plan{{Repository: "me/repo", Profile: tc.profile, EventName: tc.event, Permissions: map[string]string{"contents": "read", "packages": "write"}}}
			err := h.server.validateProfiles(plans)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatal(err)
			}
		})
	}
}
