//go:build integration

package integration_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"gopkg.in/yaml.v3"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/shitamachi/forgelet/internal/run"
)

func testGitHubAuthorizationLifecycle(t *testing.T, ctx context.Context, cluster testcontainers.Container, client *kubernetes.Clientset, httpClient *http.Client, endpoint string, request func(*testing.T, string, string, any, int) []byte, create func(*testing.T, string) run.Run, get func(*testing.T, string) run.Run) {
	t.Run("GitHub authorization lifecycle quarantine revoke restore and restart", func(t *testing.T) {
		mode := func(ctx context.Context, value string) {
			code, out, err := cluster.Exec(ctx, []string{"kubectl", "-n", "forgelet", "exec", "deployment/forgelet", "-c", "server", "--", "sh", "-ec", "printf '%s' \"$1\" > /data/github-authorization-mode", "sh", value})
			if out != nil {
				_, _ = io.Copy(io.Discard, out)
			}
			must(t, err)
			if code != 0 {
				t.Fatal("set fixture mode", code)
			}
		}
		defer func() {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
			defer cancel()
			mode(cleanupCtx, "normal")
			api(t, cleanupCtx, httpClient, endpoint, "POST", "/api/github/authorization/refresh", map[string]any{}, token, 200)
		}()
		type authorizationStatus struct {
			Snapshot struct {
				CheckedAt string `json:"checked_at"`
				Error     string `json:"error"`
			} `json:"snapshot"`
			Repositories map[string]struct {
				State string `json:"state"`
			} `json:"repositories"`
		}
		state := func() authorizationStatus {
			var v authorizationStatus
			must(t, json.Unmarshal(request(t, "GET", "/api/github/authorization", nil, 200), &v))
			return v
		}

		crossAccount := func(ctx context.Context, on bool) {
			command := []string{"touch", "/data/github-cross-account"}
			if !on {
				command = []string{"rm", "-f", "/data/github-cross-account"}
			}
			args := append([]string{"kubectl", "-n", "forgelet", "exec", "deployment/forgelet", "-c", "server", "--"}, command...)
			code, out, err := cluster.Exec(ctx, args)
			if out != nil {
				_, _ = io.Copy(io.Discard, out)
			}
			must(t, err)
			if code != 0 {
				t.Fatal("cross-account fixture failed", code)
			}
		}
		crossAccount(ctx, true)
		cm, err := client.CoreV1().ConfigMaps("forgelet").Get(ctx, "forgelet-config", metav1.GetOptions{})
		must(t, err)
		originalConfig := cm.Data["forgelet.yaml"]
		var config map[string]any
		must(t, yaml.Unmarshal([]byte(originalConfig), &config))
		config["repositories"] = append(config["repositories"].([]any), "test-other/project")
		encoded, err := yaml.Marshal(config)
		must(t, err)
		cm.Data["forgelet.yaml"] = string(encoded)
		_, err = client.CoreV1().ConfigMaps("forgelet").Update(ctx, cm, metav1.UpdateOptions{})
		must(t, err)
		restartServer(t, ctx, client, httpClient, endpoint)
		defer func() {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
			defer cancel()
			crossAccount(cleanupCtx, false)
			current, err := client.CoreV1().ConfigMaps("forgelet").Get(cleanupCtx, "forgelet-config", metav1.GetOptions{})
			must(t, err)
			current.Data["forgelet.yaml"] = originalConfig
			_, err = client.CoreV1().ConfigMaps("forgelet").Update(cleanupCtx, current, metav1.UpdateOptions{})
			must(t, err)
			restartServer(t, cleanupCtx, client, httpClient, endpoint)
		}()

		request(t, "POST", "/api/github/authorization/refresh", map[string]any{}, 200)
		baseline := state()
		if baseline.Repositories["test/project"].State != "authorized" {
			t.Fatal(baseline)
		}
		source := "on: push\njobs:\n"
		for _, name := range []string{"first", "second", "queued"} {
			source += "  " + name + ":\n    runs-on: homelab\n    steps:\n      - run: echo authorization-started; sleep 240\n"
		}
		item := create(t, source)
		var current run.Run
		waitFor(t, ctx, "two active authorization jobs", func() bool {
			current = get(t, item.ID)
			n := 0
			for _, j := range current.Jobs {
				if j.State == run.Running {
					n++
				}
			}
			return n == 2
		})
		var active run.Job
		for _, j := range current.Jobs {
			if j.State == run.Running {
				active = j
				break
			}
		}
		scoped, err := client.CoreV1().ServiceAccounts("forgelet").CreateToken(ctx, "forgelet-executor", &authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{Audiences: []string{"forgelet"}, BoundObjectRef: &authenticationv1.BoundObjectReference{Kind: "Pod", APIVersion: "v1", Name: active.PodName, UID: types.UID(active.PodUID)}}}, metav1.CreateOptions{})
		must(t, err)
		mode(ctx, "failed")
		body := `{"action":"removed","installation":{"id":7},"repositories_removed":[{"id":11}]}`
		signed := func(delivery, body string, status int) {
			mac := hmac.New(sha256.New, []byte("integration-webhook-secret"))
			_, _ = mac.Write([]byte(body))
			req, err := http.NewRequestWithContext(ctx, "POST", endpoint+"/webhooks/github", strings.NewReader(body))
			must(t, err)
			req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
			req.Header.Set("X-GitHub-Event", "installation_repositories")
			req.Header.Set("X-GitHub-Delivery", delivery)
			res, err := httpClient.Do(req)
			must(t, err)
			defer func() { _ = res.Body.Close() }()
			if res.StatusCode != status {
				t.Fatal("lifecycle status", res.StatusCode, status)
			}
		}
		signed("authorization-removal", body, 202)
		signed("authorization-removal", body, 202)
		signed("authorization-removal", `{"action":"added","installation":{"id":7}}`, 409)
		waitFor(t, ctx, "failed refresh preserves quarantine and catalog", func() bool {
			v := state()
			return v.Snapshot.Error != "" && v.Repositories["test/project"].State == "pending"
		})
		if state().Snapshot.CheckedAt != baseline.Snapshot.CheckedAt {
			t.Fatal("failed refresh replaced successful snapshot")
		}
		api(t, ctx, httpClient, endpoint, "GET", "/internal/jobs/"+active.ID+"/credentials", nil, scoped.Status.Token, 503)
		if state().Repositories["test-other/project"].State != "authorized" {
			t.Fatal("one installation hint quarantined another account")
		}
		other := createPublicWorkflow(t, ctx, httpClient, endpoint, map[string]any{"repository": "test-other/project", "sha": strings.Repeat("a", 40), "ref": "refs/heads/main", "installation_id": 8, "workflow": "on: push\njobs:\n  isolated:\n    runs-on: homelab\n    steps: [{run: echo other-account-still-authorized}]\n"})
		restartServer(t, ctx, client, httpClient, endpoint)
		if state().Repositories["test/project"].State != "pending" {
			t.Fatal("restart trusted stale grant")
		}
		after := get(t, item.ID)
		for _, j := range after.Jobs {
			if j.CancelRequested || j.State.Terminal() {
				t.Fatal("failed discovery cancelled existing work", j.State)
			}
			if j.PodUID != "" && j.PodUID != jobByKey(t, current, j.Plan.Key).PodUID {
				t.Fatal("restart replaced attempt")
			}
		}
		mode(ctx, "removed")
		request(t, "POST", "/api/github/authorization/refresh", map[string]any{}, 200)
		waitFor(t, ctx, "confirmed revoke cancels all attempts", func() bool { return get(t, item.ID).State == run.Cancelled })
		if finishRun(t, ctx, get, other.ID).State != run.Succeeded {
			t.Fatal("revoking account7 cancelled or blocked account8")
		}
		for _, j := range get(t, item.ID).Jobs {
			if j.CancelReason == "" || j.Result == nil || j.State != run.Cancelled {
				t.Fatal("revocation cancellation incomplete", j.ID, j.State)
			}
		}
		waitFor(t, ctx, "revoked pods cleaned", func() bool {
			pods, err := client.CoreV1().Pods("forgelet").List(ctx, metav1.ListOptions{LabelSelector: "forgelet.dev/job"})
			must(t, err)
			return len(pods.Items) == 0
		})
		mode(ctx, "normal")
		request(t, "POST", "/api/github/authorization/refresh", map[string]any{}, 200)
		if state().Repositories["test/project"].State != "authorized" {
			t.Fatal("grant did not recover")
		}
		restored := create(t, "on: push\njobs:\n  restored:\n    runs-on: homelab\n    steps: [{run: echo restored}]\n")
		if finishRun(t, ctx, get, restored.ID).State != run.Succeeded {
			t.Fatal("restored grant could not execute")
		}
		mode(ctx, "suspended")
		request(t, "POST", "/api/github/authorization/refresh", map[string]any{}, 200)
		if state().Repositories["test/project"].State != "suspended" {
			t.Fatal("suspension not distinguished")
		}
	})
}
