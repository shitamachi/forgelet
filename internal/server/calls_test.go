package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/executor"
	"github.com/shitamachi/forgelet/internal/run"
)

func TestReusableWorkflowThroughHTTPRealExecutorAndSQLite(t *testing.T) {
	h := newHarness(t, 2)
	h.http.Close()
	h.cfg.SecretKey = bytes.Repeat([]byte{13}, 32)
	h.start()
	secret := "call-secret-must-remain-private"
	for name, value := range map[string]string{"SOURCE": secret, "HIDDEN": "hidden-must-not-inherit"} {
		status, body := h.admin(http.MethodPut, "/api/repositories/me/repo/secrets/"+name, map[string]string{"value": value})
		if status != 200 {
			t.Fatal(status, string(body))
		}
	}
	child, err := os.ReadFile("../../test/fixtures/reusable.yml")
	if err != nil {
		t.Fatal(err)
	}
	child = []byte(strings.ReplaceAll(string(child), "runs-on: homelab", "runs-on: small"))
	child = []byte(strings.Replace(string(child), "          TOKEN: ${{ secrets.TOKEN }}", "          TOKEN: ${{ secrets.TOKEN }}\n          EVENT: ${{ toJSON(github.event) }}", 1))
	child = []byte(strings.Replace(string(child), "test -n \"$TOKEN\"", "test \"$EVENT\" = '{}'\n          test -n \"$TOKEN\"", 1))
	child = []byte(strings.Replace(string(child), "          TOKEN: ${{ secrets.TOKEN }}", "          TOKEN: ${{ secrets.TOKEN }}\n          HIDDEN: ${{ secrets.HIDDEN }}", 1))
	child = []byte(strings.Replace(string(child), "test -n \"$TOKEN\"", "test -z \"$HIDDEN\"\n          test -n \"$TOKEN\"", 1))
	child = []byte(strings.Replace(string(child), "test -n \"$TOKEN\"", "test -n \"$TOKEN\"\n          echo \"$TOKEN\"", 1))
	root := `on: push
permissions: {contents: read}
jobs:
  before:
    runs-on: small
    outputs: {label: '${{ steps.emit.outputs.label }}'}
    steps:
      - id: emit
        shell: sh
        run: echo label=from-needs >> "$GITHUB_OUTPUT"
  call:
    needs: before
    uses: ./.github/workflows/outer.yml
    with: {label: '${{ needs.before.outputs.label }}'}
    secrets: {TOKEN: '${{ secrets.SOURCE }}'}
  after:
    needs: call
    runs-on: small
    steps:
      - shell: sh
        run: test '${{ needs.call.outputs.value }}' = from-needs:2
`
	outer := `on:
  workflow_call:
    inputs: {label: {type: string, required: true}}
    secrets: {TOKEN: {required: true}}
    outputs: {value: {value: '${{ jobs.inner.outputs.value }}'}}
jobs:
  inner:
    uses: ./.github/workflows/reusable.yml
    with: {label: '${{ inputs.label }}'}
    secrets: inherit
`
	code, data := h.admin(http.MethodPost, "/api/runs", map[string]any{"repository": "me/repo", "sha": testSHA, "workflow": root, "workflow_files": map[string]string{".github/workflows/outer.yml": outer, ".github/workflows/reusable.yml": string(child)}})
	if code != 201 {
		t.Fatal(code, string(data))
	}
	var record run.Run
	if err = json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	for range 12 {
		h.tick()
		record, err = h.store.GetRun(t.Context(), record.ID)
		if err != nil {
			t.Fatal(err)
		}
		if record.State.Terminal() {
			break
		}
		for _, job := range record.Jobs {
			if job.State != run.Starting {
				continue
			}
			if job.Plan.Control != "" {
				t.Fatal("virtual job reserved")
			}
			if job.Plan.Key == "call/inner/emit" {
				creds, e := h.server.runtimeSecrets(t.Context(), job.Plan)
				if e != nil || creds["TOKEN"] != secret || creds["HIDDEN"] != "" {
					t.Fatal(creds, e)
				}
			}
			dir := t.TempDir()
			token := filepath.Join(dir, "token")
			if err = os.WriteFile(token, []byte("token-"+job.ID), 0600); err != nil {
				t.Fatal(err)
			}
			err = executor.RunRemote(t.Context(), executor.RemoteOptions{URL: h.http.URL, JobID: job.ID, PodUID: job.PodUID, TokenFile: token, Workspace: filepath.Join(dir, "workspace"), RetryTimeout: time.Second, PollInterval: 10 * time.Millisecond})
			if err != nil {
				t.Fatal(job.Plan.Key, err)
			}
		}
	}
	if record.State != run.Succeeded || h.runtime.created != 3 {
		encoded, _ := json.Marshal(record)
		t.Fatalf("state=%s pods=%d: %s", record.State, h.runtime.created, encoded)
	}
	for _, job := range record.Jobs {
		logs, err := h.store.Logs(t.Context(), job.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(logs)
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatal("secret leaked in logs")
		}
		if job.Plan.Control != "" && job.PodUID != "" {
			t.Fatal("virtual job owns pod")
		}
		if job.Plan.Key == "call" && job.Result.Outputs["value"] != "from-needs:2" {
			t.Fatal(job.Result)
		}
	}
	data, _ = json.Marshal(record)
	if bytes.Contains(data, []byte(secret)) {
		t.Fatal("secret persisted")
	}
}

func TestReusableRequiredSecretsCheckedBeforeAdmission(t *testing.T) {
	h := newHarness(t, 1)
	source := "on: push\njobs:\n  call:\n    uses: ./.github/workflows/child.yml\n    secrets: inherit\n"
	child := "on:\n  workflow_call:\n    secrets: {TOKEN: {required: true}}\njobs:\n  child:\n    runs-on: small\n    steps: [{run: echo ok}]\n"
	status, body := h.admin(http.MethodPost, "/api/runs", map[string]any{"repository": "me/repo", "sha": testSHA, "workflow": source, "workflow_files": map[string]string{".github/workflows/child.yml": child}})
	if status != 422 || !strings.Contains(string(body), "required workflow secret") {
		t.Fatal(status, string(body))
	}
}

func TestGitWorkflowSnapshotIncludesUnselectedCallees(t *testing.T) {
	h := newHarness(t, 1)
	h.server.cfg.Workflows = []string{".github/workflows/ci.yml"}
	h.provider.files = map[string][]byte{
		".github/workflows/ci.yml":    []byte("on: push\njobs:\n  call:\n    uses: ./.github/workflows/child.yml\n    with: {label: 'prefix-${{ github.ref_name }}'}\n"),
		".github/workflows/child.yml": []byte("on:\n  workflow_call:\n    inputs: {label: {type: string}}\njobs:\n  test:\n    runs-on: small\n    steps: [{run: 'echo ${{ inputs.label }}'}]\n"),
	}
	status, body := h.webhook("reusable-delivery", nil)
	if status != 202 {
		t.Fatal(status, string(body))
	}
	h.tick()
	records, err := h.store.ListRuns(t.Context())
	if err != nil || len(records) != 1 {
		t.Fatal(records, err)
	}
	record, err := h.store.GetRun(t.Context(), records[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, job := range record.Jobs {
		if job.Plan.Key == "call/test" {
			found = true
			if job.State != run.Starting || job.Plan.Inputs["label"] != "prefix-main" {
				t.Fatal(job)
			}
		}
	}
	if !found {
		t.Fatal("unselected callee absent")
	}
}

func TestReusableRerunRetainsOriginalDispatchInputs(t *testing.T) {
	h := newHarness(t, 1)
	root := `on:
  workflow_dispatch:
    inputs: {source: {type: string, required: true}}
jobs:
  call:
    uses: ./.github/workflows/child.yml
    with: {label: '${{ inputs.source }}'}
`
	child := "on:\n  workflow_call:\n    inputs: {label: {type: string}}\njobs:\n  test:\n    runs-on: small\n    steps: [{run: echo ok}]\n"
	status, data := h.admin(http.MethodPost, "/api/runs", map[string]any{"repository": "me/repo", "sha": testSHA, "workflow": root, "event_name": "workflow_dispatch", "inputs": map[string]any{"source": "original"}, "workflow_files": map[string]string{".github/workflows/child.yml": child}})
	if status != 201 {
		t.Fatal(status, string(data))
	}
	var record run.Run
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	h.tick()
	record, err := h.store.GetRun(t.Context(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range record.Jobs {
		if job.State == run.Starting {
			h.complete(job)
		}
	}
	h.tick()
	status, data = h.admin(http.MethodPost, "/api/runs/"+record.ID+"/rerun", nil)
	if status != 201 {
		t.Fatal(status, string(data))
	}
	if err = json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	h.tick()
	record, err = h.store.GetRun(t.Context(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range record.Jobs {
		if job.Plan.Key == "call/test" && (job.State != run.Starting || job.Plan.Inputs["label"] != "original") {
			t.Fatal(job)
		}
		if job.Plan.Control == "call_gate" && job.Plan.Inputs["source"] != "original" {
			t.Fatal("original trigger inputs were overwritten", job.Plan.Inputs)
		}
	}
}

type credentialRaceTransport func(*http.Request) (*http.Response, error)

func (f credentialRaceTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCancelAfterClaimBeforeSecretFetchThroughRealAPI(t *testing.T) {
	h := newHarness(t, 1)
	h.http.Close()
	h.cfg.SecretKey = bytes.Repeat([]byte{14}, 32)
	h.start()
	status, data := h.admin(http.MethodPut, "/api/repositories/me/repo/secrets/TOKEN", map[string]string{"value": "private-token"})
	if status != 200 {
		t.Fatal(status, string(data))
	}
	record := h.manual("on: push\njobs:\n  test:\n    runs-on: small\n    steps:\n      - shell: sh\n        env: {TOKEN: '${{ secrets.TOKEN }}'}\n        run: touch SHOULD_NOT_EXECUTE\n")
	h.tick()
	record, err := h.store.GetRun(t.Context(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	job := record.Jobs[0]
	client := *h.http.Client()
	client.Transport = credentialRaceTransport(func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/credentials") {
			if err := h.store.CancelRun(request.Context(), record.ID); err != nil {
				return nil, err
			}
		}
		return http.DefaultTransport.RoundTrip(request)
	})
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	if err = os.WriteFile(token, []byte("token-"+job.ID), 0600); err != nil {
		t.Fatal(err)
	}
	err = executor.RunRemote(t.Context(), executor.RemoteOptions{URL: h.http.URL, JobID: job.ID, PodUID: job.PodUID, TokenFile: token, Workspace: filepath.Join(dir, "workspace"), Client: &client, RetryTimeout: time.Second, FlushTimeout: time.Second})
	if err == nil {
		t.Fatal("cancelled executor returned success")
	}
	done, err := h.store.GetRun(t.Context(), record.ID)
	if err != nil || done.State != run.Cancelled || done.Jobs[0].Result.Error != "" {
		t.Fatal(done, err)
	}
	if _, err = os.Stat(filepath.Join(dir, "workspace", "SHOULD_NOT_EXECUTE")); !os.IsNotExist(err) {
		t.Fatal("cancelled job started code", err)
	}
}

func TestRuntimeInputHiddenInWholeObjectCannotSelectEarlyProfile(t *testing.T) {
	h := newHarness(t, 1)
	source := `on: push
jobs:
  before:
    runs-on: small
    steps: [{run: echo profile}]
  call:
    needs: before
    uses: ./.github/workflows/child.yml
    with: {profile: '${{ needs.before.outputs.profile }}'}
`
	child := `on:
  workflow_call:
    inputs: {profile: {type: string}}
jobs:
  test:
    runs-on: ${{ fromJSON(toJSON(inputs)).profile || 'small' }}
    steps: [{run: echo forbidden}]
`
	status, data := h.admin(http.MethodPost, "/api/runs", map[string]any{"repository": "me/repo", "sha": testSHA, "workflow": source, "workflow_files": map[string]string{".github/workflows/child.yml": child}})
	if status != 422 || !strings.Contains(string(data), "deferred execution settings") {
		t.Fatal(status, string(data))
	}
	rows, err := h.store.ListRuns(t.Context())
	if err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
}
