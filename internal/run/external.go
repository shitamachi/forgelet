package run

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

const ExternalLease = 60 * time.Second

const MacRunnerLabel = "macos-arm64"

func (j Job) ExternalRunnerID() string {
	if j.RunnerID == "" && j.Plan.External && j.AttemptID != "" {
		return j.Profile // Before the shared queue, the label was the device ID.
	}
	return j.RunnerID
}

// ExternalAttempt identifies a grant without exposing the bearer capability.
func ExternalAttempt(token string) string {
	value, ok := strings.CutPrefix(token, "frja_")
	if !ok || len(value) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(value); err != nil {
		return ""
	}
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:16])
}

type ExternalGrant struct {
	JobID      string `json:"job_id"`
	RunID      string `json:"run_id"`
	Repository string `json:"repository"`
	Name       string `json:"name"`
	SHA        string `json:"sha"`
	Platform   string `json:"platform,omitempty"`
	Seconds    int    `json:"lease_seconds"`
}
