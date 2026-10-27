package workflow

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/rhysd/actionlint"

	"github.com/shitamachi/forgelet/internal/run"
)

const caller = `on: push
permissions: {contents: read}
jobs:
  prepare:
    runs-on: homelab
    steps: [{run: echo prepare}]
  call:
    needs: prepare
    uses: ./.github/workflows/reusable.yml
    with: {label: '${{ needs.prepare.outputs.label }}'}
    secrets: inherit
  after:
    needs: call
    runs-on: homelab
    steps: [{run: 'echo ${{ needs.call.outputs.value }}'}]
`

func reusableSource(t *testing.T) []byte {
	t.Helper()
	source, err := os.ReadFile("../../test/fixtures/reusable.yml")
	if err != nil {
		t.Fatal(err)
	}
	return source
}
func TestReusablePlans(t *testing.T) {
	source := reusableSource(t)
	plans, err := CompileFiles([]byte(caller), testEvent, "call", map[string][]byte{".github/workflows/reusable.yml": source})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 5 {
		t.Fatalf("plans=%d", len(plans))
	}
	byKey := map[string]run.Plan{}
	for _, p := range plans {
		byKey[p.Key] = p
	}
	gate, child, result := byKey["$start/call"], byKey["call/emit"], byKey["call"]
	if gate.Control != "call_gate" || gate.CallInputs["enabled"].Value != true || child.ParentGate != gate.Key || child.RestrictSecrets || child.EventName != "push" || result.Control != "call_result" || result.NeedsAliases["call/emit"] != "emit" {
		t.Fatalf("gate=%+v child=%+v result=%+v", gate, child, result)
	}
	if len(gate.RequiredSecrets) != 1 || gate.RequiredSecrets[0] != "TOKEN" {
		t.Fatal(gate.RequiredSecrets)
	}
	if child.If != "inputs.enabled" || child.NeedsAliases["prepare"] != "" {
		t.Fatal(child)
	}
	data, _ := json.Marshal(plans)
	if strings.Contains(string(data), "runtime_secrets") {
		t.Fatal("runtime secret persisted")
	}
}
func TestReusableDiagnostics(t *testing.T) {
	good := string(reusableSource(t))
	for _, tc := range []struct{ name, root, child, want string }{
		{"missing", caller, "", "missing from the source snapshot"},
		{"missing input", strings.Replace(caller, "    with: {label: '${{ needs.prepare.outputs.label }}'}\n", "", 1), good, "required workflow input"},
		{"unknown input", strings.Replace(caller, "with: {label:", "with: {unknown:", 1), good, "undeclared workflow input"},
		{"wrong type", strings.Replace(caller, "with: {label: '${{ needs.prepare.outputs.label }}'}", "with: {label: false}", 1), good, "requires string"},
		{"secret not passed", strings.Replace(caller, "    secrets: inherit\n", "", 1), good, "required workflow secret"},
		{"remote", strings.Replace(caller, "./.github/workflows/reusable.yml", "owner/repo/.github/workflows/reusable.yml@main", 1), good, "remote calls"},
		{"not callable", caller, strings.Replace(good, "workflow_call:", "workflow_dispatch:", 1), "on.workflow_call"},
		{"unknown output", caller, strings.Replace(good, "jobs.emit.outputs.value", "jobs.missing.outputs.value", 1), "workflow output"},
		{"no escalation", strings.Replace(caller, "contents: read", "contents: none", 1), "permissions: {contents: read}\n" + good, "elevate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string][]byte{}
			if tc.child != "" {
				files[".github/workflows/reusable.yml"] = []byte(tc.child)
			}
			_, err := CompileFiles([]byte(tc.root), testEvent, "x", files)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %s got %v", tc.want, err)
			}
		})
	}
}
func TestNestedSecretBoundaryAndCycle(t *testing.T) {
	root := `on: push
jobs:
  call:
    uses: ./.github/workflows/outer.yml
    secrets: {TOKEN: '${{ secrets.SOURCE }}'}
`
	outer := `on:
  workflow_call:
    secrets: {TOKEN: {required: true}}
jobs:
  inner:
    uses: ./.github/workflows/reusable.yml
    with: {label: nested}
    secrets: inherit
`
	files := map[string][]byte{".github/workflows/outer.yml": []byte(outer), ".github/workflows/reusable.yml": reusableSource(t)}
	plans, err := CompileFiles([]byte(root), testEvent, "nested", files)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range plans {
		if p.Key == "call/inner/emit" && (!p.RestrictSecrets || p.SecretBindings["TOKEN"] != "SOURCE" || len(p.SecretBindings) != 1) {
			t.Fatal(p)
		}
	}
	files[".github/workflows/outer.yml"] = []byte("on:\n  workflow_call:\n    secrets: {TOKEN: {required: true}}\njobs:\n  cycle:\n    uses: ./.github/workflows/outer.yml\n    secrets: inherit\n")
	if _, err = CompileFiles([]byte(root), testEvent, "cycle", files); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatal(err)
	}
}
func TestSteamTasksSyntaxAndRuntimeDiagnostics(t *testing.T) {
	files := map[string][]byte{}
	for _, name := range []string{"ci.yml", "image.yml", "reliability.yml"} {
		source, err := os.ReadFile("../../test/fixtures/steam-tasks/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, errs := actionlint.Parse(source); len(errs) > 0 {
			t.Fatal(name, errs)
		}
		files[".github/workflows/"+name] = source
	}
	for _, name := range []string{"ci.yml", "image.yml"} {
		_, err := CompileFiles(files[".github/workflows/"+name], testEvent, "steam", files)
		if err != nil {
			t.Fatalf("%s must compile standard workflow before profile capability admission: %v", name, err)
		}
	}
}

func TestCallInputTemplatesAndDeferredSettings(t *testing.T) {
	child := `on:
  workflow_call:
    inputs:
      label: {type: string, default: fallback}
      flag: {type: boolean, default: false}
jobs:
  test:
    runs-on: homelab
    steps: [{run: 'echo ${{ inputs.label }}'}]
`
	source := "on: push\njobs:\n  before:\n    runs-on: homelab\n    steps: [{run: echo before}]\n  call:\n    needs: before\n    uses: ./.github/workflows/child.yml\n    with: {label: 'prefix-${{ needs.before.outputs.label }}'}\n"
	files := map[string][]byte{".github/workflows/child.yml": []byte(child)}
	if !HasCalls([]byte(source)) || HasCalls([]byte("jobs: [")) || HasCalls([]byte("on: push\njobs: {}")) {
		t.Fatal("call discovery")
	}
	if _, err := CompileFiles([]byte(source), testEvent, "templates", files); err != nil {
		t.Fatal(err)
	}
	files[".github/workflows/child.yml"] = []byte(strings.Replace(child, "runs-on: homelab", "runs-on: \"${{ fromJSON(toJSON(inputs)).label || 'homelab' }}\"", 1))
	if _, err := CompileFiles([]byte(source), testEvent, "deferred-whole-inputs", files); err == nil || !strings.Contains(err.Error(), "deferred execution settings") {
		t.Fatal(err)
	}
	files[".github/workflows/child.yml"] = []byte(strings.Replace(child, "runs-on: homelab", "runs-on: '${{ inputs.label }}'", 1))
	if _, err := CompileFiles([]byte(source), testEvent, "deferred", files); err == nil || !strings.Contains(err.Error(), "deferred execution settings") {
		t.Fatal(err)
	}
}
func TestWorkflowDefaultsPermissionsAndBundleLimits(t *testing.T) {
	source := `on: push
permissions: {contents: read}
defaults:
  run: {shell: sh, working-directory: project}
jobs:
  test:
    runs-on: homelab
    defaults: {run: {working-directory: other}}
    steps:
      - run: echo first
      - run: echo second
        shell: bash
        working-directory: explicit
`
	plans, err := Compile([]byte(source), testEvent, "defaults")
	if err != nil {
		t.Fatal(err)
	}
	if plans[0].Steps[0].Shell != "sh" || plans[0].Steps[0].WorkingDirectory != "other" || plans[0].Steps[1].Shell != "bash" || plans[0].Steps[1].WorkingDirectory != "explicit" {
		t.Fatal(plans)
	}
	for _, invalid := range []string{strings.Replace(source, "project", "/outside", 1), strings.Replace(source, "shell: sh", "shell: pwsh", 1), strings.Replace(source, "contents: read", "contents: write", 1)} {
		if _, err = Compile([]byte(invalid), testEvent, "bad"); err == nil {
			t.Fatal("accepted invalid execution defaults")
		}
	}
	for _, files := range []map[string][]byte{{"../outside.yml": []byte("x")}, {".github/workflows/large.yml": make([]byte, 4<<20+1)}} {
		if _, err = CompileFiles([]byte(source), testEvent, "bad", files); err == nil {
			t.Fatal("bundle limit ignored")
		}
	}
}

func TestJobPermissionOverridesDefaultWithinCallCeiling(t *testing.T) {
	root := "on: push\npermissions: {}\njobs:\n  test:\n    permissions: {contents: read}\n    runs-on: homelab\n    steps: [{run: echo ok}]\n"
	plans, err := Compile([]byte(root), testEvent, "override")
	if err != nil || plans[0].Permissions["contents"] != "read" {
		t.Fatal(plans, err)
	}
	root = "on: push\npermissions: {contents: none}\njobs:\n  call:\n    uses: ./.github/workflows/child.yml\n"
	child := "on: workflow_call\njobs:\n  test:\n    permissions: {contents: read}\n    runs-on: homelab\n    steps: [{run: echo forbidden}]\n"
	if _, err = CompileFiles([]byte(root), testEvent, "ceiling", map[string][]byte{".github/workflows/child.yml": []byte(child)}); err == nil || !strings.Contains(err.Error(), "elevate") {
		t.Fatal(err)
	}
}

func TestDeferredInputReferencesUseASTInsteadOfStringMatching(t *testing.T) {
	source := `on: push
jobs:
  before:
    runs-on: homelab
    steps: [{run: echo before}]
  call:
    needs: before
    uses: ./.github/workflows/outer.yml
    with: {payload: '${{ needs[''before''].outputs.payload }}'}
`
	outer := `on:
  workflow_call:
    inputs: {payload: {type: string}}
jobs:
  inner:
    uses: ./.github/workflows/inner.yml
    with: {enabled: '${{ fromJSON(inputs.payload) }}'}
`
	inner := `on:
  workflow_call:
    inputs: {enabled: {type: boolean}}
jobs:
  test:
    runs-on: homelab
    if: inputs.enabled
    steps: [{run: echo enabled}]
`
	if _, err := CompileFiles([]byte(source), testEvent, "deferred-ast", map[string][]byte{".github/workflows/outer.yml": []byte(outer), ".github/workflows/inner.yml": []byte(inner)}); err != nil {
		t.Fatal(err)
	}
}
