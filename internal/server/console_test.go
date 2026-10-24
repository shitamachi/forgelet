package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/kube"
	"github.com/shitamachi/forgelet/internal/run"
)

func openHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, 4)
	h.http.Close()
	h.cfg.AdminAccess, h.cfg.AdminToken = "open", ""
	h.cfg.SecretKey = bytes.Repeat([]byte{7}, 32)
	h.start()
	return h
}

func TestConsoleAccessMustBeExplicit(t *testing.T) {
	h := newHarness(t, 4)
	for _, tc := range []struct {
		mode, token string
		valid       bool
	}{
		{"", "", false}, {"token", "", false}, {"disabled", testAdmin, false}, {"open", "", true}, {"token", testAdmin, true}, {"", testAdmin, true},
	} {
		cfg := h.cfg
		cfg.AdminAccess, cfg.AdminToken = tc.mode, tc.token
		if _, err := New(cfg, h.store, h.runtime, h.provider); (err == nil) != tc.valid {
			t.Errorf("mode=%q token present=%v: %v", tc.mode, tc.token != "", err)
		}
	}
}

func TestOpenConsoleMutationsAndIndependentWorkloadIdentity(t *testing.T) {
	h := openHarness(t)
	headers := map[string]string{"Content-Type": "application/json", "Origin": h.http.URL, "Sec-Fetch-Site": "same-origin"}
	for _, path := range []string{"/api/config", "/api/status", "/api/runs", "/api/events"} {
		if code, body := h.request("GET", path, "", nil); code != 200 {
			t.Fatalf("%s: %d %s", path, code, body)
		}
	}
	input, _ := json.Marshal(map[string]any{"repository": "me/repo", "sha": testSHA, "ref": "refs/heads/main", "workflow": testWorkflow})
	code, body := h.request("POST", "/api/runs", string(input), headers)
	if code != 201 {
		t.Fatalf("create: %d %s", code, body)
	}
	var item run.Run
	if err := json.Unmarshal(body, &item); err != nil {
		t.Fatal(err)
	}
	if code, body = h.request("POST", "/api/runs/"+item.ID+"/cancel", "{}", headers); code != 200 {
		t.Fatalf("cancel: %d %s", code, body)
	}
	h.tick()
	saved, err := h.store.GetRun(t.Context(), item.ID)
	if err != nil || saved.State != run.Cancelled {
		t.Fatalf("cancel not durable: %+v %v", saved, err)
	}
	if code, body = h.request("POST", "/api/runs/"+item.ID+"/rerun", "{}", headers); code != 201 {
		t.Fatalf("rerun: %d %s", code, body)
	}
	for _, kind := range []string{"vars", "secrets"} {
		path := "/api/repositories/me/repo/" + kind
		if code, body = h.request("PUT", path+"/TOKEN", `{"value":"console-test-value"}`, headers); code != 200 {
			t.Fatalf("save: %d %s", code, body)
		}
		code, body = h.request("GET", path, "", nil)
		if code != 200 || !strings.Contains(string(body), "TOKEN") || (kind == "secrets" && strings.Contains(string(body), "console-test-value")) {
			t.Fatalf("read %s: %d %s", kind, code, body)
		}
		if code, body = h.request("DELETE", path+"/TOKEN", "{}", headers); code != 200 {
			t.Fatalf("delete: %d %s", code, body)
		}
	}
	if code, _ = h.request("POST", "/webhooks/github", "{}", headers); code != 401 {
		t.Fatalf("open mode bypassed webhook verification: %d", code)
	}
	if code, _ = h.request("POST", "/internal/jobs/"+item.Jobs[0].ID+"/claim", "{}", headers); code != 401 {
		t.Fatalf("open mode bypassed workload identity: %d", code)
	}
}

func TestOpenConsoleRejectsCrossOriginAndFormWrites(t *testing.T) {
	h := openHarness(t)
	for _, tc := range []struct {
		name, origin, site, content string
		want                        int
	}{
		{"same origin", h.http.URL, "same-origin", "application/json; charset=utf-8", 200},
		{"CLI", "", "", "application/json", 200},
		{"foreign site", "https://evil.invalid", "cross-site", "application/json", 403},
		{"foreign origin", "https://evil.invalid", "", "application/json", 403},
		{"opaque origin", "null", "", "application/json", 403},
		{"wrong scheme", strings.Replace(h.http.URL, "http:", "https:", 1), "", "application/json", 403},
		{"same host with path", h.http.URL + "/path", "", "application/json", 403},
		{"form", "", "", "application/x-www-form-urlencoded", 415},
		{"simple text", "", "", "text/plain", 415},
		{"no type", "", "", "", 415},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := h.request("PUT", "/api/repositories/me/repo/vars/ORIGIN_TEST", `{"value":"test"}`, map[string]string{"Origin": tc.origin, "Sec-Fetch-Site": tc.site, "Content-Type": tc.content})
			if code != tc.want {
				t.Fatalf("write: %d %s", code, body)
			}
		})
	}
}

func TestConsoleConfigurationExcludesCredentials(t *testing.T) {
	h := openHarness(t)
	h.server.cfg.Profiles["builder"] = kube.Profile{Image: "executor:test", Build: &kube.BuildProfile{RegistrySecret: "private-registry-secret"}}
	code, body := h.request("GET", "/api/config", "", nil)
	if code != 200 {
		t.Fatalf("config: %d %s", code, body)
	}
	for _, secret := range []string{h.cfg.WebhookSecret, testAdmin, "private-registry-secret", "secret_key", "admin_token"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("config leaked %q", secret)
		}
	}
	var result struct {
		Profiles []struct {
			Name  string
			Build bool
		}
		Repositories []string
		MaxParallel  int `json:"max_parallel"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Profiles) != 2 || result.Profiles[0].Name != "builder" || !result.Profiles[0].Build || result.MaxParallel != 4 || result.Repositories[0] != "me/repo" {
		t.Fatalf("config: %s", body)
	}
}

func TestConsoleDownloadsAllLogPagesAndReportsExpiration(t *testing.T) {
	h := openHarness(t)
	item := h.manual(testWorkflow)
	h.tick()
	item, err := h.store.GetRun(t.Context(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	job := item.Jobs[0]
	if code, body := h.runtimeRequest("POST", "claim", job, nil); code != 200 {
		t.Fatalf("claim: %d %s", code, body)
	}
	var want strings.Builder
	for seq := 1; seq <= 600; seq++ {
		data := fmt.Sprintf("chunk-%d\n", seq)
		want.WriteString(data)
		if err := h.store.AppendLog(t.Context(), job.ID, job.PodUID, run.Log{Seq: int64(seq), StepID: "test", Data: data}); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequestWithContext(t.Context(), "GET", h.http.URL+"/api/jobs/"+job.ID+"/logs/download", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := h.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != 200 || string(data) != want.String() || !strings.Contains(resp.Header.Get("Content-Disposition"), "attachment;") || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("download: %d %v len=%d", resp.StatusCode, err, len(data))
	}
	if code, body := h.runtimeRequest("POST", "result", job, run.Result{State: run.Succeeded, LastLogSeq: 600}); code != 200 {
		t.Fatalf("finish: %d %s", code, body)
	}
	h.tick()
	now := time.Now().Add(8 * 24 * time.Hour)
	h.server.now = func() time.Time { return now }
	h.tick()
	if code, body := h.request("GET", "/api/jobs/"+job.ID+"/logs/download", "", nil); code != 410 {
		t.Fatalf("expired download: %d %s", code, body)
	}
	if code, _ := h.request("GET", "/api/jobs/missing/logs/download", "", nil); code != 404 {
		t.Fatalf("missing download: %d", code)
	}
}
