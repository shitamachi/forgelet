//go:build integration

package integration_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run"
	"github.com/shitamachi/forgelet/internal/server"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func testWebConsole(t *testing.T, ctx context.Context, client *kubernetes.Clientset, httpClient *http.Client, endpoint, root string) {
	t.Run("embedded web console with real browser and restart", func(t *testing.T) {
		cm, err := client.CoreV1().ConfigMaps("forgelet").Get(ctx, "forgelet-config", metav1.GetOptions{})
		must(t, err)
		original := cm.Data["forgelet.yaml"]
		cm.Data["forgelet.yaml"] = strings.Replace(original, "admin_access: token", "admin_access: open", 1)
		_, err = client.CoreV1().ConfigMaps("forgelet").Update(ctx, cm, metav1.UpdateOptions{})
		must(t, err)
		defer func() {
			restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
			defer cancel()
			current, err := client.CoreV1().ConfigMaps("forgelet").Get(restoreCtx, "forgelet-config", metav1.GetOptions{})
			must(t, err)
			current.Data["forgelet.yaml"] = original
			_, err = client.CoreV1().ConfigMaps("forgelet").Update(restoreCtx, current, metav1.UpdateOptions{})
			must(t, err)
			restartServer(t, restoreCtx, client, httpClient, endpoint)
		}()
		restartServer(t, ctx, client, httpClient, endpoint)
		var baseline server.Management
		must(t, json.Unmarshal(api(t, ctx, httpClient, endpoint, "GET", "/api/management", nil, "", 200), &baseline))
		// The test browser reaches the actual embedded bundle and Go/SQLite API.
		// No fixture server, route interception, user's kubeconfig or auth cookie.
		cmd := exec.CommandContext(ctx, "npm", "run", "test:e2e:live")
		cmd.Dir = filepath.Join(root, "web")
		cmd.Env = append(os.Environ(), "FORGELET_E2E_BASE_URL="+endpoint)
		output, err := cmd.CombinedOutput()
		t.Logf("browser integration:\n%s", output)
		must(t, err)

		api(t, ctx, httpClient, endpoint, "PUT", "/api/repositories/test/project/vars/CONSOLE_RESTART", map[string]string{"value": "persistent"}, "", 200)
		restartServer(t, ctx, client, httpClient, endpoint)
		if body := api(t, ctx, httpClient, endpoint, "GET", "/api/repositories/test/project/vars", nil, "", 200); !strings.Contains(string(body), `"CONSOLE_RESTART":"persistent"`) {
			t.Fatalf("console write lost after restart: %s", body)
		}
		var managed server.Management
		must(t, json.Unmarshal(api(t, ctx, httpClient, endpoint, "GET", "/api/management", nil, "", 200), &managed))
		if len(managed.Workflows) == 0 {
			t.Fatal("managed workflow lost after restart")
		}
		// A signed push after restart must use the saved YAML and create a real Pod.
		payload := fmt.Sprintf(`{"ref":"refs/heads/main","after":"%s","repository":{"full_name":"test/console"},"installation":{"id":7}}`, strings.Repeat("b", 40))
		mac := hmac.New(sha256.New, []byte("integration-webhook-secret"))
		_, err = mac.Write([]byte(payload))
		must(t, err)
		push, err := http.NewRequestWithContext(ctx, "POST", endpoint+"/webhooks/github", strings.NewReader(payload))
		must(t, err)
		push.Header.Set("X-GitHub-Event", "push")
		push.Header.Set("X-GitHub-Delivery", "managed-config-after-restart")
		push.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		pushed, err := httpClient.Do(push)
		must(t, err)
		must(t, pushed.Body.Close())
		if pushed.StatusCode != 202 {
			t.Fatal(pushed.StatusCode)
		}
		waitFor(t, ctx, "configured webhook executes after restart", func() bool {
			var runs []run.Run
			must(t, json.Unmarshal(api(t, ctx, httpClient, endpoint, "GET", "/api/runs", nil, "", 200), &runs))
			for _, r := range runs {
				if r.DeliveryID == "managed-config-after-restart" {
					if r.WorkflowPath != ".github/workflows/console.yml" {
						t.Fatal(r.WorkflowPath)
					}
					return r.State == run.Succeeded
				}
			}
			return false
		})
		baseline.Version = managed.Version
		api(t, ctx, httpClient, endpoint, "PUT", "/api/management", baseline, "", 200)
		for _, path := range []string{"/", "/runs", "/deployments", "/repositories", "/system", "/connections", "/workflows", "/targets"} {
			req, err := http.NewRequestWithContext(ctx, "GET", endpoint+path, nil)
			must(t, err)
			resp, err := httpClient.Do(req)
			must(t, err)
			body, readErr := io.ReadAll(resp.Body)
			must(t, resp.Body.Close())
			must(t, readErr)
			if resp.StatusCode != 200 || !strings.Contains(string(body), "/assets/") || resp.Header.Get("Cache-Control") != "no-store" {
				t.Fatalf("embedded deep link after restart %s: %d %s", path, resp.StatusCode, body)
			}
		}
		req, err := http.NewRequestWithContext(ctx, "PUT", endpoint+"/api/repositories/test/project/vars/CONSOLE_RESTART", strings.NewReader(`{"value":"foreign"}`))
		must(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "https://foreign.invalid")
		resp, err := httpClient.Do(req)
		must(t, err)
		must(t, resp.Body.Close())
		if resp.StatusCode != 403 {
			t.Fatalf("cross-origin write accepted: %d", resp.StatusCode)
		}
		api(t, ctx, httpClient, endpoint, "POST", "/internal/jobs/unknown/claim", nil, "", 401)
		api(t, ctx, httpClient, endpoint, "POST", "/webhooks/github", nil, "", 401)
		api(t, ctx, httpClient, endpoint, "DELETE", "/api/repositories/test/project/vars/CONSOLE_RESTART", nil, "", 200)
	})
}
