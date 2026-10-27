//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"k8s.io/client-go/kubernetes"

	"github.com/shitamachi/forgelet/internal/run"
)

func testWorkflowCalls(t *testing.T, ctx context.Context, cluster testcontainers.Container, client *kubernetes.Clientset, httpClient *http.Client, endpoint, root string, request func(*testing.T, string, string, any, int) []byte, get func(*testing.T, string) run.Run) {
	t.Run("reusable workflow typed inputs secrets outputs survive restart", func(t *testing.T) {
		secret := "workflow-call-private-token"
		request(t, "PUT", "/api/repositories/test/project/secrets/TOKEN", map[string]string{"value": secret}, 200)
		defer request(t, "DELETE", "/api/repositories/test/project/secrets/TOKEN", nil, 200)
		source, err := os.ReadFile(filepath.Join(root, "test/fixtures/reusable.yml"))
		must(t, err)
		source = []byte(strings.Replace(string(source), "          TOKEN: ${{ secrets.TOKEN }}", "          TOKEN: ${{ secrets.TOKEN }}\n          EVENT: ${{ github.event.inputs.prefix || 'prefix' }}", 1))
		source = []byte(strings.Replace(string(source), "test -n \"$TOKEN\"", "test \"$EVENT\" = prefix\n          test '${{ github.job }}' = emit\n          test '${{ github.event_name }}' = workflow_dispatch\n          test -n \"$TOKEN\"", 1))
		child := strings.Replace(string(source), "test -n \"$TOKEN\"", "test -n \"$TOKEN\"\n          echo \"$TOKEN\"\n          echo call-ready\n          while [ ! -f /workspace/call-continue ]; do sleep 1; done", 1)
		caller := `on:
  workflow_dispatch:
    inputs: {prefix: {type: string, default: prefix}}
permissions: {contents: read}
jobs:
  before:
    runs-on: homelab
    outputs: {label: '${{ steps.emit.outputs.label }}', enabled: '${{ steps.emit.outputs.enabled }}'}
    steps:
      - id: emit
        shell: sh
        run: |
          echo label=from-real-pod >> "$GITHUB_OUTPUT"
          echo enabled=true >> "$GITHUB_OUTPUT"
  call:
    needs: before
    uses: ./.github/workflows/outer.yml
    with:
      label: ${{ format('{0}-{1}', inputs.prefix, needs['before'].outputs.label) }}
      enabled: ${{ fromJSON(needs['before'].outputs.enabled) }}
    secrets: inherit
  after:
    needs: call
    runs-on: homelab
    steps:
      - shell: sh
        run: test '${{ needs.call.outputs.value }}' = prefix-from-real-pod:2
`
		outer := `on:
  workflow_call:
    inputs: {label: {type: string, required: true}, enabled: {type: boolean, default: false}}
    secrets: {TOKEN: {required: true}}
    outputs: {value: {value: '${{ jobs.inner.outputs.value }}'}}
jobs:
  inner:
    uses: ./.github/workflows/child.yml
    with: {label: '${{ inputs.label }}', enabled: '${{ inputs.enabled }}'}
    secrets: inherit
`
		body := map[string]any{"repository": "test/project", "sha": strings.Repeat("a", 40), "workflow": caller, "event_name": "workflow_dispatch", "workflow_files": map[string]string{".github/workflows/outer.yml": outer, ".github/workflows/child.yml": child}}
		var record run.Run
		must(t, json.Unmarshal(request(t, "POST", "/api/runs", body, 201), &record))
		var active run.Job
		waitFor(t, ctx, "called job is executing", func() bool {
			current := get(t, record.ID)
			active = jobByKey(t, current, "call/inner/emit")
			return active.State == run.Running
		})
		restartServer(t, ctx, client, httpClient, endpoint)
		current := get(t, record.ID)
		if jobByKey(t, current, "call/inner/emit").PodUID != active.PodUID {
			t.Fatal("call attempt replayed after restart")
		}
		clusterCommand(t, ctx, cluster, "kubectl", "-n", "forgelet", "exec", active.PodName, "--", "touch", "/workspace/call-continue")
		done := finishRun(t, ctx, get, record.ID)
		if done.State != run.Succeeded || jobByKey(t, done, "call").Result.Outputs["value"] != "prefix-from-real-pod:2" {
			data, _ := json.Marshal(done)
			t.Fatalf("call failed: %s", data)
		}
		pods := 0
		for _, job := range done.Jobs {
			if job.Plan.Control != "" {
				if job.PodUID != "" {
					t.Fatal("control job created Pod")
				}
			} else if job.PodUID != "" {
				pods++
			}
		}
		if pods != 3 {
			t.Fatalf("expected three execution attempts, got %d", pods)
		}
		logs := string(request(t, "GET", "/api/jobs/"+active.ID+"/logs", nil, 200))
		if strings.Contains(logs, secret) || !strings.Contains(logs, "***") {
			t.Fatal("called secret was not masked")
		}
		// Whole-object access must not hide a needs-derived early profile.
		invalidChild := strings.Replace(child, "runs-on: homelab", "runs-on: ${{ fromJSON(toJSON(inputs)).label || 'homelab' }}", 1)
		rejected := map[string]any{}
		for key, value := range body {
			rejected[key] = value
		}
		rejected["workflow_files"] = map[string]string{".github/workflows/outer.yml": outer, ".github/workflows/child.yml": invalidChild}
		diagnostic := string(request(t, "POST", "/api/runs", rejected, 422))
		if !strings.Contains(diagnostic, "deferred execution settings") {
			t.Fatal(diagnostic)
		}
		// Rerun persists the same expanded graph, resets gates, and can be cancelled
		// while a nested called Pod is active.
		var rerun run.Run
		must(t, json.Unmarshal(request(t, "POST", "/api/runs/"+record.ID+"/rerun", nil, 201), &rerun))
		waitFor(t, ctx, "rerun called job starts", func() bool { return jobByKey(t, get(t, rerun.ID), "call/inner/emit").State == run.Running })
		rerunActive := jobByKey(t, get(t, rerun.ID), "call/inner/emit")
		if rerunActive.Plan.Inputs["label"] != "prefix-from-real-pod" {
			t.Fatal("rerun lost original dispatch inputs", rerunActive.Plan.Inputs)
		}
		request(t, "POST", "/api/runs/"+rerun.ID+"/cancel", nil, 200)
		cancelled := finishRun(t, ctx, get, rerun.ID)
		if cancelled.State != run.Cancelled || jobByKey(t, cancelled, "after").State != run.Cancelled {
			t.Fatalf("nested cancellation: %s", cancelled.State)
		}
	})
	t.Run("reusable workflow failure and skipped gate propagate", func(t *testing.T) {
		for _, skip := range []bool{false, true} {
			caller := "on: push\njobs:\n  call:\n    uses: ./.github/workflows/child.yml\n  after:\n    needs: call\n    runs-on: homelab\n    steps: [{run: echo forbidden}]\n"
			if skip {
				caller = strings.Replace(caller, "    uses:", "    if: false\n    uses:", 1)
			}
			child := "on: workflow_call\njobs:\n  fail:\n    runs-on: homelab\n    steps: [{shell: sh, run: exit 7}]\n  cleanup:\n    needs: fail\n    if: always()\n    runs-on: homelab\n    steps: [{shell: sh, run: echo cleanup}]\n"
			var record run.Run
			must(t, json.Unmarshal(request(t, "POST", "/api/runs", map[string]any{"repository": "test/project", "sha": strings.Repeat("a", 40), "workflow": caller, "workflow_files": map[string]string{".github/workflows/child.yml": child}}, 201), &record))
			done := finishRun(t, ctx, get, record.ID)
			want := run.Failed
			if skip {
				want = run.Skipped
			}
			if done.State != want || jobByKey(t, done, "call").State != want || jobByKey(t, done, "after").State != run.Skipped {
				data, _ := json.Marshal(done)
				t.Fatalf("propagation: %s", data)
			}
			if skip && jobByKey(t, done, "call/cleanup").PodUID != "" {
				t.Fatal("always escaped skipped call")
			}
		}
	})
}
