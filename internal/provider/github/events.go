package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/shitamachi/forgelet/internal/run"
)

var (
	// ErrIgnored is returned for push events which intentionally do not execute.
	ErrIgnored        = errors.New("github: ignored push event")
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	shaPattern        = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)
)

func validRepository(repository string) bool {
	if !repositoryPattern.MatchString(repository) || len(repository) > 256 {
		return false
	}
	for _, part := range strings.Split(repository, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	return true
}

func validSHA(sha string) bool {
	return shaPattern.MatchString(sha) && sha != strings.Repeat("0", 40)
}

// ParsePush extracts repository identity and an immutable commit. It deliberately
// ignores payload-provided URLs; network destinations come from configuration.
func ParsePush(body []byte) (run.Event, error) {
	var payload struct {
		Ref        string `json:"ref"`
		After      string `json:"after"`
		Before     string `json:"before"`
		Deleted    bool   `json:"deleted"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
	}
	if len(body) > maxResponseBytes {
		return run.Event{}, errors.New("github: push payload exceeds size limit")
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return run.Event{}, errors.New("github: invalid push payload JSON")
	}
	if !validRepository(payload.Repository.FullName) || payload.Installation.ID <= 0 {
		return run.Event{}, errors.New("github: invalid push repository or installation")
	}
	if payload.Deleted {
		return run.Event{}, ErrIgnored
	}
	validRef := strings.HasPrefix(payload.Ref, "refs/heads/") && len(payload.Ref) > len("refs/heads/") || strings.HasPrefix(payload.Ref, "refs/tags/") && len(payload.Ref) > len("refs/tags/")
	if !validRef || strings.ContainsAny(payload.Ref, "\x00\r\n\t ") || strings.Contains(payload.Ref, "..") || !validSHA(payload.After) {
		return run.Event{}, errors.New("github: invalid push ref or commit SHA")
	}
	return run.Event{BeforeSHA: payload.Before, Repository: payload.Repository.FullName, SHA: strings.ToLower(payload.After), Ref: payload.Ref, InstallationID: payload.Installation.ID, Payload: append(json.RawMessage(nil), body...)}, nil
}

// VerifySignature verifies GitHub's X-Hub-Signature-256 in constant time.
func VerifySignature(body []byte, signature, secret string) bool {
	if secret == "" || !strings.HasPrefix(signature, "sha256=") || len(signature) != 7+64 {
		return false
	}
	want, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hmac.Equal(want, mac.Sum(nil))
}
