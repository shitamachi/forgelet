//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/shitamachi/forgelet/internal/server"
)

func testAuthentication(t *testing.T, ctx context.Context, cluster testcontainers.Container, client *kubernetes.Clientset, httpClient *http.Client, endpoint, root string) {
	t.Run("administrator and GitHub login with repository authorization and restart", func(t *testing.T) {
		cm, err := client.CoreV1().ConfigMaps("forgelet").Get(ctx, "forgelet-config", metav1.GetOptions{})
		must(t, err)
		original := cm.Data["forgelet.yaml"]
		var cfg map[string]any
		must(t, yaml.Unmarshal([]byte(original), &cfg))
		cfg["admin_access"] = "session"
		cfg["public_url"] = endpoint
		cfg["auth"] = map[string]string{"bootstrap_password_file": "/auth-test/password", "github_client_id": "integration-client", "github_client_secret_file": "/auth-test/client-secret", "github_web_url": "https://localhost:9443"}
		data, err := yaml.Marshal(cfg)
		must(t, err)
		cm.Data["forgelet.yaml"] = string(data)
		_, err = client.CoreV1().Secrets("forgelet").Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "forgelet-login-test"}, Data: map[string][]byte{"password": []byte("integration-admin-password-12345"), "client-secret": []byte("integration-oauth-client-secret")}}, metav1.CreateOptions{})
		must(t, err)
		deployment, err := client.AppsV1().Deployments("forgelet").Get(ctx, "forgelet", metav1.GetOptions{})
		must(t, err)
		originalSpec := deployment.Spec.Template.Spec.DeepCopy()
		deployment.Spec.Template.Spec.Volumes = append(deployment.Spec.Template.Spec.Volumes, corev1.Volume{Name: "auth-test", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "forgelet-login-test"}}})
		deployment.Spec.Template.Spec.Containers[0].VolumeMounts = append(deployment.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "auth-test", MountPath: "/auth-test", ReadOnly: true})
		// The existing test credentials and App are isolated fixtures. Only the new
		// external GitHub authorize page is simulated; application routes remain real.
		_, err = client.CoreV1().ConfigMaps("forgelet").Update(ctx, cm, metav1.UpdateOptions{})
		must(t, err)
		_, err = client.AppsV1().Deployments("forgelet").Update(ctx, deployment, metav1.UpdateOptions{})
		must(t, err)
		defer func() {
			restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
			defer cancel()
			current, err := client.CoreV1().ConfigMaps("forgelet").Get(restoreCtx, "forgelet-config", metav1.GetOptions{})
			must(t, err)
			current.Data["forgelet.yaml"] = original
			_, err = client.CoreV1().ConfigMaps("forgelet").Update(restoreCtx, current, metav1.UpdateOptions{})
			must(t, err)
			d, err := client.AppsV1().Deployments("forgelet").Get(restoreCtx, "forgelet", metav1.GetOptions{})
			must(t, err)
			d.Spec.Template.Spec = *originalSpec
			_, err = client.AppsV1().Deployments("forgelet").Update(restoreCtx, d, metav1.UpdateOptions{})
			must(t, err)
			waitFor(t, restoreCtx, "legacy access restored", func() bool { return authMode(restoreCtx, httpClient, endpoint) == "token" })
		}()
		waitFor(t, ctx, "session authentication active", func() bool { return authMode(ctx, httpClient, endpoint) == "session" })
		api(t, ctx, httpClient, endpoint, "GET", "/api/config", nil, token, 401)
		jar, err := cookiejar.New(nil)
		must(t, err)
		authenticated := *httpClient
		authenticated.Jar = jar
		api(t, ctx, &authenticated, endpoint, "POST", "/api/auth/login", map[string]string{"username": "admin", "password": "integration-admin-password-12345"}, "", 200)
		var baseline server.Management
		must(t, json.Unmarshal(api(t, ctx, &authenticated, endpoint, "GET", "/api/management", nil, "", 200), &baseline))
		// Test-only external consent, separate from the denied OAuth mutation API.
		consent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/approve" {
				http.NotFound(w, r)
				return
			}
			code, output, err := cluster.Exec(r.Context(), []string{"kubectl", "-n", "forgelet", "exec", "deployment/forgelet", "-c", "server", "--", "touch", "/data/github-oauth-grants.json"})
			if output != nil {
				_, _ = io.Copy(io.Discard, output)
			}
			if err != nil || code != 0 {
				http.Error(w, "test consent failed", 500)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}))
		defer consent.Close()
		cmd := exec.CommandContext(ctx, "npm", "run", "test:e2e:auth")
		cmd.Dir = filepath.Join(root, "web")
		cmd.Env = append(os.Environ(), "FORGELET_E2E_BASE_URL="+endpoint, "FORGELET_E2E_CONSENT_URL="+consent.URL+"/approve")
		output, err := cmd.CombinedOutput()
		t.Logf("authentication browser integration:\n%s", output)
		must(t, err)
		restartServer(t, ctx, client, httpClient, endpoint)
		var session struct {
			User struct {
				GitHubID int64 `json:"github_id"`
			} `json:"user"`
		}
		must(t, json.Unmarshal(api(t, ctx, &authenticated, endpoint, "GET", "/api/auth/session", nil, "", 200), &session))
		if session.User.GitHubID != 987654 {
			t.Fatal("GitHub identity or session lost after restart", session)
		}
		repos := api(t, ctx, &authenticated, endpoint, "GET", "/api/github/user/repositories", nil, "", 200)
		if len(repos) == 0 {
			t.Fatal("GitHub credential lost after restart")
		}
		var managed server.Management
		must(t, json.Unmarshal(api(t, ctx, &authenticated, endpoint, "GET", "/api/management", nil, "", 200), &managed))
		found := false
		for _, r := range managed.Repositories {
			found = found || r.Name == "test/oauth" && r.RepositoryID == 14 && r.InstallationID == 7
		}
		if !found {
			t.Fatal("OAuth imported repository lost after restart")
		}
		// Password rotation must revoke a live cookie in the real server and must
		// not be undone when the bootstrap Secret is read again after a restart.
		origin, err := url.Parse(endpoint)
		must(t, err)
		oldJar, err := cookiejar.New(nil)
		must(t, err)
		oldJar.SetCookies(origin, authenticated.Jar.Cookies(origin))
		oldClient := authenticated
		oldClient.Jar = oldJar
		api(t, ctx, &authenticated, endpoint, "POST", "/api/auth/password", map[string]string{"current_password": "integration-admin-password-12345", "new_password": "integration-admin-password-updated"}, "", 200)
		api(t, ctx, &oldClient, endpoint, "GET", "/api/config", nil, "", 401)
		restartServer(t, ctx, client, httpClient, endpoint)
		api(t, ctx, &authenticated, endpoint, "GET", "/api/config", nil, "", 200)
		api(t, ctx, &authenticated, endpoint, "POST", "/api/auth/login", map[string]string{"username": "admin", "password": "integration-admin-password-updated"}, "", 200)
		api(t, ctx, httpClient, endpoint, "POST", "/api/auth/login", map[string]string{"username": "admin", "password": "integration-admin-password-12345"}, "", 401)
		// A forged/cross-browser callback burns state without exchanging credentials.
		var flow struct {
			URL string `json:"url"`
		}
		must(t, json.Unmarshal(api(t, ctx, httpClient, endpoint, "POST", "/api/auth/github/start", map[string]bool{"link": false}, "", 200), &flow))
		authorize, err := url.Parse(flow.URL)
		must(t, err)
		api(t, ctx, httpClient, endpoint, "GET", "/api/auth/github/callback?code=integration-oauth-code&state="+authorize.Query().Get("state"), nil, "", 400)
		baseline.Version = managed.Version
		api(t, ctx, &authenticated, endpoint, "PUT", "/api/management", baseline, "", 200)
		api(t, ctx, &authenticated, endpoint, "POST", "/api/auth/logout", map[string]any{}, "", 200)
		api(t, ctx, &authenticated, endpoint, "GET", "/api/config", nil, "", 401)
		api(t, ctx, httpClient, endpoint, "POST", "/internal/jobs/unknown/claim", nil, "", 401)
		api(t, ctx, httpClient, endpoint, "POST", "/webhooks/github", nil, "", 401)
	})
}

func authMode(ctx context.Context, client *http.Client, endpoint string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/api/auth/session", nil)
	if err != nil {
		return ""
	}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return ""
	}
	var value struct {
		Mode string `json:"mode"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&value) != nil {
		return ""
	}
	return value.Mode
}
