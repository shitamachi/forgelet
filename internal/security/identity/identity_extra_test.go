package identity

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestHasScope(t *testing.T) {
	id := Identity{Scopes: []string{ScopePlanRead}}
	if !id.HasScope(ScopePlanRead) {
		t.Error("carried scope not found")
	}
	if id.HasScope(ScopeStatusWrite) {
		t.Error("uncarried scope found")
	}
	if (Identity{}).HasScope(ScopePlanRead) {
		t.Error("empty identity carried a scope")
	}
}

func TestIssueRejectsEmptyBindings(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	iss := NewLocalIssuer(issueKey, func() time.Time { return now })
	ctx := context.Background()

	noNS := validIdentity(now)
	noNS.Namespace = ""
	if _, err := iss.Issue(ctx, noNS); err == nil {
		t.Error("missing namespace must fail")
	}
	noJob := validIdentity(now)
	noJob.JobRunID = ""
	if _, err := iss.Issue(ctx, noJob); err == nil {
		t.Error("missing job run must fail")
	}
}

func TestVerifyMalformedParts(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	ver := NewLocalVerifier(issueKey, func() time.Time { return now }, time.Minute)
	ctx := context.Background()

	// Valid shape, undecodable payload.
	if _, err := ver.Verify(ctx, base64.RawURLEncoding.EncodeToString([]byte("%%%"))+".AAAA"); err == nil {
		t.Error("undecodable payload accepted")
	}
	// Undecodable claims JSON.
	payload := base64.RawURLEncoding.EncodeToString([]byte("not-json"))
	if _, err := ver.Verify(ctx, payload+".AAAA"); err == nil {
		t.Error("non-JSON claims accepted")
	}
	// Undecodable signature half.
	iss := NewLocalIssuer(issueKey, func() time.Time { return now })
	raw, err := iss.Issue(ctx, validIdentity(now))
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	parts := strings.Split(raw, ".")
	if _, err := ver.Verify(ctx, parts[0]+".%%%"); err == nil {
		t.Error("undecodable signature accepted")
	}
}

func TestMemoryNonceCacheDefaultClock(t *testing.T) {
	cache := NewMemoryNonceCache(time.Minute, nil)
	if !cache.Claim("x") {
		t.Fatal("default clock must work")
	}
}
