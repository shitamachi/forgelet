package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

const testSHA = "1234567890abcdef1234567890abcdef12345678"

func TestVerifySignature(t *testing.T) {
	body := []byte(`{"ref":"refs/heads/main"}`)
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write(body)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	for _, tc := range []struct {
		name, signature, secret string
		body                    []byte
		valid                   bool
	}{
		{"valid", signature, "secret", body, true},
		{"wrong secret", signature, "wrong", body, false},
		{"changed body", signature, "secret", []byte("changed"), false},
		{"empty secret", signature, "", body, false},
		{"wrong algorithm", "sha1=" + signature[7:], "secret", body, false},
		{"wrong length", "sha256=123", "secret", body, false},
		{"invalid hex", "sha256=" + strings.Repeat("z", 64), "secret", body, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := VerifySignature(tc.body, tc.signature, tc.secret); got != tc.valid {
				t.Fatalf("got %v want %v", got, tc.valid)
			}
		})
	}
}

func TestParsePush(t *testing.T) {
	valid := `{"ref":"refs/heads/main","after":"` + testSHA + `","repository":{"full_name":"owner/repo","clone_url":"http://attacker.internal/repo"},"installation":{"id":7}}`
	event, err := ParsePush([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if event.Repository != "owner/repo" || event.SHA != testSHA || event.InstallationID != 7 || event.Ref != "refs/heads/main" || string(event.Payload) != valid {
		t.Fatalf("event=%+v", event)
	}
	for _, tc := range []struct {
		name, body string
		ignored    bool
	}{
		{"malformed", `{`, false},
		{"missing repository", `{"ref":"refs/heads/main"}`, false},
		{"traversal", strings.Replace(valid, "owner/repo", "../repo", 1), false},
		{"zero installation", strings.Replace(valid, `"id":7`, `"id":0`, 1), false},
		{"mutable SHA", strings.Replace(valid, testSHA, "main", 1), false},
		{"zero SHA", strings.Replace(valid, testSHA, strings.Repeat("0", 40), 1), false},
		{"invalid ref", strings.Replace(valid, "refs/heads/main", "main", 1), false},
		{"deleted", strings.Replace(valid, `"ref":`, `"deleted":true,"ref":`, 1), true},
		{"oversized", strings.Repeat(" ", maxResponseBytes+1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParsePush([]byte(tc.body))
			if err == nil {
				t.Fatal("expected rejection")
			}
			if tc.ignored && !errors.Is(err, ErrIgnored) {
				t.Fatalf("expected ignored, got %v", err)
			}
		})
	}
	if _, err := ParsePush([]byte(strings.Replace(valid, "refs/heads/main", "refs/tags/v1", 1))); err != nil {
		t.Fatal(err)
	}
}
