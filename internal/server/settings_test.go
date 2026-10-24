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

func TestSecretCipherBindsRepositoryAndName(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	a, err := sealSecret(key, "me/repo", "KEY", "sensitive")
	if err != nil || bytes.Contains(a, []byte("sensitive")) {
		t.Fatal(err)
	}
	b, err := sealSecret(key, "me/repo", "KEY", "sensitive")
	if err != nil || bytes.Equal(a, b) {
		t.Fatal("nonces reused")
	}
	plain, err := openSecret(key, "me/repo", "KEY", a)
	if err != nil || plain != "sensitive" {
		t.Fatal(plain, err)
	}
	for _, tc := range []struct {
		repo, name string
		data, key  []byte
	}{{"other/repo", "KEY", a, key}, {"me/repo", "OTHER", a, key}, {"me/repo", "KEY", a, bytes.Repeat([]byte{8}, 32)}, {"me/repo", "KEY", a[:2], key}} {
		if _, err := openSecret(tc.key, tc.repo, tc.name, tc.data); err == nil {
			t.Fatal("accepted modified binding or ciphertext")
		}
	}
}

func TestRepositorySecretsAndVarsThroughRealRemoteExecutor(t *testing.T) {
	h := newHarness(t, 2)
	h.http.Close()
	h.cfg.SecretKey = bytes.Repeat([]byte{11}, 32)
	h.start()
	secret := "unique-test-secret-do-not-leak"
	for _, entry := range []struct{ kind, name, value string }{{"secrets", "TOKEN", secret}, {"secrets", "UNUSED", "unrelated"}, {"vars", "TARGET", "local"}} {
		status, body := h.admin(http.MethodPut, "/api/repositories/me/repo/"+entry.kind+"/"+entry.name, map[string]string{"value": entry.value})
		if status != 200 {
			t.Fatal(status, string(body))
		}
	}
	encrypted, err := h.store.Settings(t.Context(), "me/repo", "secrets")
	if err != nil || bytes.Contains(encrypted["TOKEN"], []byte(secret)) {
		t.Fatal("secret stored in plaintext", err)
	}
	status, body := h.admin(http.MethodGet, "/api/repositories/me/repo/secrets", nil)
	if status != 200 || bytes.Contains(body, []byte(secret)) || !bytes.Contains(body, []byte("TOKEN")) {
		t.Fatal(status, string(body))
	}
	item := h.manual(`on: push
jobs:
  test:
    runs-on: small
    env:
      TOKEN: ${{ secrets.TOKEN }}
      TARGET: ${{ vars.TARGET }}
    steps:
      - shell: sh
        run: |
          test "$TARGET" = local
          test -n "$TOKEN"
          echo "$TOKEN"
          echo value="$TOKEN" >> "$GITHUB_OUTPUT"
`)
	encoded, _ := json.Marshal(item)
	if bytes.Contains(encoded, []byte(secret)) {
		t.Fatal("secret in persisted plan")
	}
	h.tick()
	item, err = h.store.GetRun(t.Context(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	job := item.Jobs[0]
	credentials, err := h.server.runtimeSecrets(t.Context(), job.Plan)
	if err != nil || credentials["TOKEN"] != secret || credentials["UNUSED"] != "" {
		t.Fatal("incorrect credential scope", err)
	}
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("token-"+job.ID), 0600); err != nil {
		t.Fatal(err)
	}
	if err := executor.RunRemote(t.Context(), executor.RemoteOptions{URL: h.http.URL, JobID: job.ID, PodUID: job.PodUID, TokenFile: tokenFile, Workspace: filepath.Join(dir, "workspace"), RetryTimeout: time.Second, PollInterval: 10 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	finished, err := h.store.GetJob(t.Context(), job.ID)
	if err != nil || finished.State != run.Succeeded || len(finished.Result.Steps[0].Outputs) != 0 {
		t.Fatal(finished, err)
	}
	logs, err := h.store.Logs(t.Context(), job.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(logs)
	if bytes.Contains(data, []byte(secret)) || !strings.Contains(string(data), "***") {
		t.Fatal(string(data))
	}
	status, _ = h.admin(http.MethodDelete, "/api/repositories/me/repo/secrets/TOKEN", nil)
	if status != 200 {
		t.Fatal(status)
	}
}
