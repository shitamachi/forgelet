//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/testcontainers/testcontainers-go"

	"github.com/shitamachi/forgelet/internal/run"
)

func testPhaseTwo(t *testing.T, ctx context.Context, cluster testcontainers.Container, client *kubernetes.Clientset, httpClient *http.Client, endpoint, root string, request func(*testing.T, string, string, any, int) []byte, create func(*testing.T, string) run.Run, get func(*testing.T, string) run.Run) {
	logs := func(t *testing.T, job run.Job) string {
		var entries []run.Log
		must(t, json.Unmarshal(request(t, "GET", "/api/jobs/"+job.ID+"/logs", nil, 200), &entries))
		var out strings.Builder
		for _, entry := range entries {
			out.WriteString(entry.Data)
		}
		return out.String()
	}
	finish := func(t *testing.T, item run.Run) run.Run {
		var result run.Run
		waitFor(t, ctx, "phase 2 workflow finishes", func() bool { result = get(t, item.ID); return result.State.Terminal() })
		return result
	}
	t.Run("signed push filters and same repository PR merge SHA", func(t *testing.T) {
		send := func(event, delivery string, payload map[string]any) {
			body, err := json.Marshal(payload)
			must(t, err)
			mac := hmac.New(sha256.New, []byte("integration-webhook-secret"))
			_, _ = mac.Write(body)
			req, err := http.NewRequestWithContext(ctx, "POST", endpoint+"/webhooks/github", bytes.NewReader(body))
			must(t, err)
			req.Header.Set("X-GitHub-Event", event)
			req.Header.Set("X-GitHub-Delivery", delivery)
			req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
			resp, err := httpClient.Do(req)
			must(t, err)
			defer func() { _ = resp.Body.Close() }()
			raw, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != 202 {
				t.Fatalf("webhook=%d %s", resp.StatusCode, raw)
			}
		}
		find := func(delivery string) run.Run {
			var runs []run.Run
			must(t, json.Unmarshal(request(t, "GET", "/api/runs", nil, 200), &runs))
			for _, item := range runs {
				if item.DeliveryID == delivery {
					return get(t, item.ID)
				}
			}
			return run.Run{}
		}
		push := map[string]any{"ref": "refs/heads/main", "before": strings.Repeat("b", 40), "after": strings.Repeat("c", 40), "repository": map[string]string{"full_name": "test/project"}, "installation": map[string]int{"id": 7}}
		send("push", "integration-push", push)
		var pushed run.Run
		waitFor(t, ctx, "signed push executes", func() bool { pushed = find("integration-push"); return pushed.ID != "" && pushed.State.Terminal() })
		if pushed.State != run.Succeeded {
			t.Fatal(pushed)
		}
		pr := map[string]any{"action": "synchronize", "number": 12, "repository": map[string]string{"full_name": "test/project"}, "installation": map[string]int{"id": 7}, "pull_request": map[string]any{"merge_commit_sha": strings.Repeat("d", 40), "head": map[string]any{"sha": strings.Repeat("e", 40), "repo": map[string]any{"full_name": "test/project", "fork": false}}, "base": map[string]any{"sha": strings.Repeat("b", 40), "ref": "main", "repo": map[string]string{"full_name": "test/project"}}}}
		send("pull_request", "integration-pr", pr)
		var merged run.Run
		waitFor(t, ctx, "PR merge executes", func() bool { merged = find("integration-pr"); return merged.ID != "" && merged.State.Terminal() })
		if merged.State != run.Succeeded || merged.SHA != strings.Repeat("d", 40) || !strings.Contains(logs(t, merged.Jobs[0]), "event=pull_request sha="+strings.Repeat("d", 40)) {
			t.Fatal(merged)
		}
		// GitHub often sends opened before its background test merge exists.
		clusterCommand(t, ctx, cluster, "kubectl", "-n", "forgelet", "exec", "deployment/forgelet", "-c", "github-test-api", "--", "touch", "/fixture-state/pr-merge-pending")
		pr["action"] = "opened"
		pr["pull_request"].(map[string]any)["merge_commit_sha"] = nil
		send("pull_request", "integration-pr-pending", pr)
		waitFor(t, ctx, "unresolved PR durably waiting", func() bool {
			var events []run.Event
			must(t, json.Unmarshal(request(t, "GET", "/api/events", nil, 200), &events))
			for _, e := range events {
				if e.DeliveryID == "integration-pr-pending" {
					return e.SHA == "" && strings.Contains(e.Error, "not ready")
				}
			}
			return false
		})
		if find("integration-pr-pending").ID != "" {
			t.Fatal("admitted PR without merge SHA")
		}
		restartServer(t, ctx, client, httpClient, endpoint)
		clusterCommand(t, ctx, cluster, "kubectl", "-n", "forgelet", "exec", "deployment/forgelet", "-c", "github-test-api", "--", "rm", "/fixture-state/pr-merge-pending")
		var resolved run.Run
		waitFor(t, ctx, "pending PR resolves after restart", func() bool {
			resolved = find("integration-pr-pending")
			return resolved.ID != "" && resolved.State.Terminal()
		})
		if resolved.State != run.Succeeded || resolved.SHA != strings.Repeat("d", 40) {
			t.Fatal(resolved)
		}
		send("pull_request", "integration-pr-pending", pr)
		if repeated := find("integration-pr-pending"); repeated.ID != resolved.ID {
			t.Fatal("duplicate PR was replayed", repeated)
		}
		push["ref"] = "refs/heads/ignored"
		send("push", "integration-filtered", push)
		pr["pull_request"].(map[string]any)["head"].(map[string]any)["repo"] = map[string]any{"full_name": "attacker/fork", "fork": true}
		send("pull_request", "integration-fork", pr)
		// A later accepted event is a processing barrier for the filtered event.
		push["ref"] = "refs/heads/main"
		send("push", "integration-barrier", push)
		waitFor(t, ctx, "later event finishes", func() bool { return find("integration-barrier").State.Terminal() })
		waitFor(t, ctx, "filtered event acknowledged", func() bool {
			var events []run.Event
			must(t, json.Unmarshal(request(t, "GET", "/api/events", nil, 200), &events))
			for _, event := range events {
				if event.DeliveryID == "integration-fork" {
					t.Fatal("fork was persisted")
				}
				if event.DeliveryID == "integration-filtered" {
					if event.Error != "" {
						t.Fatal(event.Error)
					}
					return true
				}
			}
			return false
		})
		if find("integration-filtered").ID != "" || find("integration-fork").ID != "" {
			t.Fatal("filtered or fork event executed")
		}
	})
	t.Run("matrix quota outputs and job continue on error", func(t *testing.T) {
		item := create(t, `on: push
jobs:
  test:
    runs-on: homelab
    continue-on-error: ${{ matrix.experimental }}
    strategy:
      max-parallel: 1
      matrix:
        include:
          - {name: one, experimental: false}
          - {name: two, experimental: false}
          - {name: experimental, experimental: true}
    outputs:
      value: ${{ steps.work.outputs.value }}
    steps:
      - id: work
        run: |
          echo 'value=${{ matrix.name }}' >> "$GITHUB_OUTPUT"
          sleep 2
          test '${{ matrix.experimental }}' = false
  join:
    needs: test
    runs-on: homelab
    steps:
      - run: test '${{ needs.test.result }}' = success
`)
		sawQueue := false
		waitFor(t, ctx, "matrix obeys quota", func() bool {
			item = get(t, item.ID)
			active, queued := 0, 0
			for _, j := range item.Jobs {
				if j.Plan.Group == "test" {
					if j.State == run.Running || j.State == run.Starting {
						active++
					}
					if j.State == run.Queued {
						queued++
					}
				}
			}
			if active > 1 {
				t.Fatalf("matrix quota exceeded: %d", active)
			}
			sawQueue = sawQueue || active == 1 && queued > 0
			return item.State.Terminal()
		})
		if item.State != run.Succeeded || !sawQueue {
			t.Fatal(item)
		}
		ignored := false
		for _, j := range item.Jobs {
			if j.Plan.ContinueOnError {
				ignored = true
				if j.State != run.Succeeded || j.Result.State != run.Failed {
					t.Fatal("raw failure not preserved", j)
				}
			}
		}
		if !ignored {
			t.Fatal("experimental variant missing")
		}
	})
	t.Run("matrix fail fast cancels active and pending variants", func(t *testing.T) {
		// Scheduling order is not guaranteed. Exercise pending and active
		// cancellation separately, without assuming which matrix ID starts first.
		for _, tc := range []struct {
			strategy, script  string
			cancelled, active int
		}{
			{"{max-parallel: 1, matrix: {index: [1, 2, 3]}}", "exit 5", 2, 0},
			{"{max-parallel: 2, matrix: {index: [1, 2]}}", "if [ '${{ matrix.index }}' = 1 ]; then sleep 3; exit 5; fi; sleep 90", 1, 1},
		} {
			item := finish(t, create(t, "on: push\njobs:\n  failing:\n    runs-on: homelab\n    strategy: "+tc.strategy+"\n    steps:\n      - run: |\n          "+tc.script+"\n"))
			cancelled, active := 0, 0
			for _, j := range item.Jobs {
				if j.State == run.Cancelled {
					cancelled++
					if j.PodUID != "" {
						active++
					}
				}
			}
			if item.State != run.Failed || cancelled != tc.cancelled || active != tc.active {
				t.Fatalf("strategy=%s cancelled=%d active=%d run=%+v", tc.strategy, cancelled, active, item)
			}
		}
	})
	t.Run("concurrency cancellation survives restart", func(t *testing.T) {
		source := `on: push
concurrency: {group: deployment, cancel-in-progress: true}
jobs:
  test:
    runs-on: homelab
    steps: [{run: 'echo old; sleep 90'}]
`
		old := create(t, source)
		waitFor(t, ctx, "old group active", func() bool { return get(t, old.ID).Jobs[0].State == run.Running })
		newest := create(t, strings.Replace(source, "echo old; sleep 90", "echo newest", 1))
		restartServer(t, ctx, client, httpClient, endpoint)
		old = finish(t, old)
		newest = finish(t, newest)
		if old.State != run.Cancelled || newest.State != run.Succeeded || !strings.Contains(logs(t, newest.Jobs[0]), "newest") {
			t.Fatal(old, newest)
		}
	})
	t.Run("real Node 20 Node 24 composite and post lifecycle", func(t *testing.T) {
		source, err := os.ReadFile(filepath.Join(root, "test/fixtures/actions.yml"))
		must(t, err)
		item := finish(t, create(t, string(source)))
		a, f := jobByKey(t, item, "actions"), jobByKey(t, item, "failure")
		if a.State != run.Succeeded || f.State != run.Failed {
			for _, j := range item.Jobs {
				t.Logf("job %s=%+v logs=%s", j.Plan.Key, j.Result, logs(t, j))
			}
			t.Fatal(item.State)
		}
		output := logs(t, a)
		for _, expected := range []string{"action-outputs-ok", "node20-runtime=v20.", "post-first", "post-second"} {
			if !strings.Contains(output, expected) {
				t.Fatalf("missing %s: %s", expected, output)
			}
		}
		if strings.Index(output, "post-second") > strings.Index(output, "post-first") {
			t.Fatal("post order", output)
		}
		if out := logs(t, f); !strings.Contains(out, "failure-cleanup") || !strings.Contains(out, "post-failure") || !strings.Contains(out, "pre-observed-job-failure") {
			t.Fatal(out)
		}
	})
	t.Run("repository encrypted secrets vars and typed dispatch", func(t *testing.T) {
		secret := "k3s-synthetic-secret-not-a-real-credential"
		request(t, "PUT", "/api/repositories/test/project/secrets/TOKEN", map[string]string{"value": secret}, 200)
		request(t, "PUT", "/api/repositories/test/project/vars/TARGET", map[string]string{"value": "homelab"}, 200)
		source := `on:
  workflow_dispatch:
    inputs:
      enabled: {type: boolean, default: false}
      count: {type: number, default: 2}
jobs:
  test:
    runs-on: ${{ vars.TARGET }}
    if: inputs.enabled && inputs.count > 1
    env: {TOKEN: '${{ secrets.TOKEN }}'}
    steps:
      - run: |
          test -n "$TOKEN"
          echo "$TOKEN"
          echo typed-inputs-ok
`
		body := request(t, "POST", "/api/runs", map[string]any{"repository": "test/project", "sha": strings.Repeat("a", 40), "ref": "refs/heads/main", "workflow": source, "event_name": "workflow_dispatch", "inputs": map[string]any{"enabled": true}}, 201)
		if bytes.Contains(body, []byte(secret)) {
			t.Fatal("secret leaked into plan")
		}
		var item run.Run
		must(t, json.Unmarshal(body, &item))
		item = finish(t, item)
		out := logs(t, item.Jobs[0])
		if item.State != run.Succeeded || strings.Contains(out, secret) || !strings.Contains(out, "***") || !strings.Contains(out, "typed-inputs-ok") {
			t.Fatal(item.State, out)
		}
		request(t, "DELETE", "/api/repositories/test/project/secrets/TOKEN", nil, 200)
	})
	t.Run("actual pinned setup go and setup node toolchains", func(t *testing.T) {
		source, err := os.ReadFile(filepath.Join(root, "test/fixtures/toolchains.yml"))
		must(t, err)
		preload := loadToolchainPreload(t, ctx, client, root)
		item := create(t, preload.workflow(string(source)))
		preload.stage(t, ctx, cluster, item, get)
		defer func() {
			if !t.Failed() {
				return
			}
			item = get(t, item.ID)
			for _, job := range item.Jobs {
				t.Logf("toolchain job %s: state=%s result=%+v\n%s", job.ID, job.State, job.Result, logs(t, job))
			}
			if !item.State.Terminal() {
				request(t, "POST", "/api/runs/"+item.ID+"/cancel", nil, 200)
				waitFor(t, ctx, "failed toolchain test cancels active run", func() bool { return get(t, item.ID).State.Terminal() })
			}
		}()
		// These Actions use real toolchains (optional verified preload). Allow the twelve-minute
		// deadline plus scheduling time, independently of local state transitions.
		waitForTimeout(t, ctx, "real toolchains finish", 13*time.Minute, func() bool {
			item = get(t, item.ID)
			return item.State.Terminal()
		})
		out := logs(t, item.Jobs[0])
		if item.State != run.Succeeded || !strings.Contains(out, "real-go-binary-ok") || !strings.Contains(out, "real-node-binary-ok") {
			t.Fatalf("toolchains: %+v\n%s", item.Jobs[0].Result, out)
		}
	})
}
