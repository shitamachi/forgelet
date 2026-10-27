package server

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/shitamachi/forgelet/internal/kube"
)

func TestActAdmissionSnapshotsBackendAndScopesDependencyTokens(t *testing.T) {
	h := newHarness(t, 1)
	h.http.Close()
	h.cfg.Profiles["actions"] = kube.Profile{Image: "executor:act", Act: &kube.ActProfile{}}
	h.cfg.RepositoryTokens = RepositoryTokens{"me/repo": {"SDK": "me/sdk", "UNUSED": "other/repo"}}
	h.start()
	item := h.manual(`on: push
jobs:
  test:
    runs-on: actions
    env:
      SDK: ${{ secrets.SDK }}
    steps:
      - uses: actions/setup-go@v5
        with: {go-version: '1.27', cache: true}
`)
	if item.Jobs[0].Plan.Kernel != "act" {
		t.Fatal("backend was not snapshotted")
	}
	h.tick()
	job := item.Jobs[0]
	if code, _ := h.runtimeRequest(http.MethodGet, "credentials", job, nil); code != 409 {
		t.Fatal("credentials before claim", code)
	}
	if code, _ := h.runtimeRequest(http.MethodPost, "claim", job, nil); code != 200 {
		t.Fatal(code)
	}
	code, body := h.runtimeRequest(http.MethodGet, "credentials", job, nil)
	if code != 200 || !bytes.Contains(body, []byte(`"SDK":"repository-scoped-clone-token"`)) {
		t.Fatal(code, string(body))
	}
	if strings.Join(h.provider.tokenRequests, ",") != "42:me/sdk,42:me/repo" {
		t.Fatal(h.provider.tokenRequests)
	}
	settings, err := h.store.Settings(t.Context(), "me/repo", "secrets")
	if err != nil || len(settings) != 0 {
		t.Fatal("minted token was persisted", err)
	}
	if code, _ := h.admin(http.MethodPost, "/api/runs/"+item.ID+"/cancel", nil); code != 200 {
		t.Fatal(code)
	}
	if code, _ := h.runtimeRequest(http.MethodGet, "credentials", job, nil); code != 409 {
		t.Fatal("cancelled job obtained credentials", code)
	}
	if len(h.provider.tokenRequests) != 2 {
		t.Fatal("minted after cancellation")
	}
}

func TestActAndNativeCapabilitiesAreExplicit(t *testing.T) {
	h := newHarness(t, 1)
	source := []byte(`on: push
jobs:
  test:
    runs-on: small
    steps:
      - uses: actions/setup-go@v5
`)
	code, body := h.admin(http.MethodPost, "/api/runs", map[string]any{"repository": "me/repo", "sha": strings.Repeat("a", 40), "workflow_path": ".github/workflows/ci.yml", "workflow": string(source)})
	if code != 422 || !strings.Contains(string(body), "act execution profile") {
		t.Fatal(code, string(body))
	}
}

func TestPowerShellRequiresExplicitActProfile(t *testing.T) {
	h := newHarness(t, 1)
	h.server.cfg.Profiles["actions"] = kube.Profile{Image: "executor:act", Act: &kube.ActProfile{}}
	for _, tc := range []struct {
		profile, defaults string
		want              int
	}{
		{"small", "", 422}, {"small", "defaults: {run: {shell: pwsh}}\n", 422},
		{"actions", "", 201}, {"actions", "defaults: {run: {shell: pwsh}}\n", 201},
	} {
		shell := "        shell: pwsh\n"
		if tc.defaults != "" {
			shell = ""
		}
		source := "on: push\n" + tc.defaults + "jobs:\n  test:\n    runs-on: " + tc.profile + "\n    steps:\n      - run: Write-Output 'pwsh-ready'\n" + shell
		code, body := h.admin("POST", "/api/runs", map[string]any{"repository": "me/repo", "sha": strings.Repeat("a", 40), "workflow": source})
		if code != tc.want || tc.want == 422 && !bytes.Contains(body, []byte("pwsh requires an act")) {
			t.Fatal(code, string(body))
		}
	}
}

func TestActContentsNoneDoesNotMintImplicitToken(t *testing.T) {
	h := newHarness(t, 1)
	h.http.Close()
	h.cfg.Profiles["actions"] = kube.Profile{Image: "executor:act", Act: &kube.ActProfile{}}
	h.start()
	item := h.manual(`on: push
permissions: {}
jobs:
  none:
    runs-on: actions
    steps:
      - run: echo no implicit token
`)
	h.tick()
	job := item.Jobs[0]
	if code, _ := h.runtimeRequest(http.MethodPost, "claim", job, nil); code != 200 {
		t.Fatal(code)
	}
	code, body := h.runtimeRequest(http.MethodGet, "credentials", job, nil)
	if code != 200 || bytes.Contains(body, []byte("token")) || len(h.provider.tokenRequests) != 0 {
		t.Fatal(code, string(body), h.provider.tokenRequests)
	}
}

func TestProfileSnapshotOwnsItsImageMirrorCatalog(t *testing.T) {
	original := map[string]kube.Profile{"act": {Image: "zot.example/runner", Act: &kube.ActProfile{ImageMirrors: map[string]string{"alpine:3.22": "original"}}}}
	snapshot := cloneProfiles(original)
	original["act"].Act.ImageMirrors["alpine:3.22"] = "changed"
	if snapshot["act"].Act.ImageMirrors["alpine:3.22"] != "original" {
		t.Fatal("caller changed server mirror catalog")
	}
	snapshot["act"].Act.ImageMirrors["other:1"] = "local"
	if _, ok := original["act"].Act.ImageMirrors["other:1"]; ok {
		t.Fatal("snapshot reused caller's map")
	}
}
