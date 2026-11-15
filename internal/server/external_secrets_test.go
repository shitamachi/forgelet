package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/shitamachi/forgelet/internal/run"
)

// A device job that needs one repository secret, expressed the way a GitHub
// workflow does.
const runnerSecretWorkflow = `on: push
jobs:
  mac:
    runs-on: macos-arm64
    steps:
      - run: echo "$TOKEN"
        env:
          TOKEN: ${{ secrets.TOKEN }}
`

func submitWorkflow(t *testing.T, h *harness, source string) (int, []byte) {
	t.Helper()
	return h.admin(http.MethodPost, "/api/runs", map[string]any{
		"repository": "me/repo", "sha": testSHA, "ref": "refs/heads/main",
		"workflow": source, "installation_id": 42,
	})
}

// Secrets stay on cluster Pods until an administrator lists the repository in
// runner_secrets. Admission happens before any device is considered, so the
// failure is a clear diagnostic rather than a job that waits forever.
func TestRunnerSecretsRequireRepositoryOptIn(t *testing.T) {
	h := externalHarnessWith(t, 1, func(h *harness) {
		h.cfg.SecretKey = bytes.Repeat([]byte{9}, 32)
	})
	code, body := submitWorkflow(t, h, runnerSecretWorkflow)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("device job with secrets admitted: %d %s", code, body)
	}
	if text := string(body); !strings.Contains(text, "runner_secrets") || !strings.Contains(text, "me/repo") {
		t.Fatalf("diagnostic should name the switch and repository: %s", text)
	}
}

// The same workflow still runs on a cluster Pod: the switch routes work, it does
// not disable repository secrets.
func TestRunnerSecretsKeepPodJobsUnchanged(t *testing.T) {
	h := externalHarnessWith(t, 1, func(h *harness) {
		h.cfg.SecretKey = bytes.Repeat([]byte{9}, 32)
	})
	pod := h.manual(strings.Replace(runnerSecretWorkflow, "runs-on: macos-arm64", "runs-on: small", 1))
	h.tick()
	job, err := h.store.GetJob(t.Context(), pod.Jobs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Plan.External || h.runtime.created != 1 {
		t.Fatalf("Pod job should run unchanged: external=%v pods=%d", job.Plan.External, h.runtime.created)
	}
}

// An opted-in repository gets the secret through the existing attempt-token
// channel, after claim, exactly like a Pod does.
func TestOptedInRepositoryDeliversSecretsToDevice(t *testing.T) {
	h := externalHarnessWith(t, 1, func(h *harness) {
		h.cfg.SecretKey = bytes.Repeat([]byte{9}, 32)
		h.cfg.RunnerSecrets = []string{"ME/Repo"} // repository matching ignores case
	})
	sealed, err := sealSecret(h.cfg.SecretKey, "me/repo", "TOKEN", "s3cret-value")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.PutSetting(t.Context(), "me/repo", "secrets", "TOKEN", sealed); err != nil {
		t.Fatal(err)
	}
	item := h.manual(runnerSecretWorkflow)
	attempt := "frja_" + strings.Repeat("b", 64)
	code, body := h.externalRequest("/poll", testMachineToken, runnerRequest{Attempt: attempt})
	if code != http.StatusOK {
		t.Fatalf("poll: %d %s", code, body)
	}
	var grant run.ExternalGrant
	if err := json.Unmarshal(body, &grant); err != nil {
		t.Fatal(err)
	}
	if grant.JobID != item.Jobs[0].ID {
		t.Fatalf("granted %q, want %q", grant.JobID, item.Jobs[0].ID)
	}
	if code, body := h.externalRequest("/jobs/"+grant.JobID+"/claim", attempt, nil); code != http.StatusOK {
		t.Fatalf("claim: %d %s", code, body)
	}
	code, body = h.request(http.MethodGet, "/runner/v1/jobs/"+grant.JobID+"/credentials", "", map[string]string{
		"Authorization": "Bearer " + attempt,
	})
	if code != http.StatusOK {
		t.Fatalf("credentials: %d %s", code, body)
	}
	var credentials struct {
		Secrets map[string]string `json:"secrets"`
	}
	if err := json.Unmarshal(body, &credentials); err != nil {
		t.Fatal(err)
	}
	if credentials.Secrets["TOKEN"] != "s3cret-value" {
		t.Fatalf("secret not delivered to the device: %s", body)
	}
}
