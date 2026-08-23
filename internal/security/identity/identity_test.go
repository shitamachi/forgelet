package identity

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run/model"
)

var issueKey = []byte("0123456789abcdef0123456789abcdef") // 32 bytes

func validIdentity(now time.Time) Identity {
	return Identity{
		Audience:  Audience,
		Namespace: "forgelet-jobs",
		PodUID:    "pod-uid-1",
		JobRunID:  model.JobRunID("01JTEST0000000000000000001"),
		Scopes:    []string{ScopePlanRead, ScopeSecretsRead, ScopeStatusWrite},
		TokenID:   "tid-1",
		IssuedAt:  now,
		ExpiresAt: now.Add(10 * time.Minute),
	}
}

func TestIssueVerifyRoundtrip(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	iss := NewLocalIssuer(issueKey, func() time.Time { return now })
	ver := NewLocalVerifier(issueKey, func() time.Time { return now.Add(time.Minute) }, 0)

	raw, err := iss.Issue(context.Background(), validIdentity(now))
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	got, err := ver.Verify(context.Background(), raw)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.JobRunID != model.JobRunID("01JTEST0000000000000000001") ||
		got.Namespace != "forgelet-jobs" || got.PodUID != "pod-uid-1" ||
		!got.HasScope(ScopeSecretsRead) || got.TokenID != "tid-1" {
		t.Errorf("roundtrip lost claims: %+v", got)
	}
}

func TestIssueMintsTokenID(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	iss := NewLocalIssuer(issueKey, func() time.Time { return now })
	id := validIdentity(now)
	id.TokenID = ""
	raw, err := iss.Issue(context.Background(), id)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	got, err := NewLocalVerifier(issueKey, func() time.Time { return now }, 0).Verify(context.Background(), raw)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.TokenID == "" {
		t.Error("issuer did not mint a token id")
	}
}

func TestIssueRejectsBadClaims(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	iss := NewLocalIssuer(issueKey, func() time.Time { return now })
	ctx := context.Background()

	wrongAud := validIdentity(now)
	wrongAud.Audience = "somebody-else"
	if _, err := iss.Issue(ctx, wrongAud); err == nil {
		t.Error("wrong audience must be rejected at issue")
	}

	noBindings := validIdentity(now)
	noBindings.PodUID = ""
	if _, err := iss.Issue(ctx, noBindings); err == nil {
		t.Error("missing pod uid must be rejected at issue")
	}

	tooLong := validIdentity(now)
	tooLong.ExpiresAt = now.Add(MaxTTL + time.Minute)
	if _, err := iss.Issue(ctx, tooLong); err == nil {
		t.Error("expiry beyond MaxTTL must be rejected at issue")
	}

	noExpiry := validIdentity(now)
	noExpiry.ExpiresAt = time.Time{}
	if _, err := iss.Issue(ctx, noExpiry); err == nil {
		t.Error("missing expiry must be rejected at issue")
	}
}

func TestVerifyRejections(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	iss := NewLocalIssuer(issueKey, func() time.Time { return now })
	raw, err := iss.Issue(context.Background(), validIdentity(now))
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	ctx := context.Background()

	cases := []struct {
		name string
		raw  string
		now  time.Time
		key  []byte
	}{
		{"expired", raw, now.Add(11 * time.Minute), issueKey},
		{"issued in future", raw, now.Add(-5 * time.Minute), issueKey},
		{"wrong signing key", raw, now, []byte("ffffffffffffffffffffffffffffffff")},
		{"garbage", "not-a-token", now, issueKey},
		{"empty", "", now, issueKey},
		{"three parts", raw + ".extra", now, issueKey},
	}
	for _, tc := range cases {
		ver := NewLocalVerifier(tc.key, func() time.Time { return tc.now }, 0)
		if _, err := ver.Verify(ctx, tc.raw); err == nil {
			t.Errorf("%s: verify succeeded, want failure", tc.name)
		}
	}

	// Audience tampering: re-sign claims with a foreign audience using the
	// real key; verification must still refuse.
	var claims Identity
	payload, sig, err := splitToken(raw)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	_ = sig
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	claims.Audience = "kubernetes.default.svc"
	foreign, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	mac := hmac.New(sha256.New, issueKey)
	mac.Write(foreign)
	tampered := base64.RawURLEncoding.EncodeToString(foreign) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	ver := NewLocalVerifier(issueKey, func() time.Time { return now }, 0)
	if _, err := ver.Verify(ctx, tampered); !errors.Is(err, ErrInvalidToken) || !strings.Contains(err.Error(), "audience") {
		t.Errorf("foreign audience accepted: err=%v", err)
	}
}

func TestMemoryNonceCache(t *testing.T) {
	mu := sync.Mutex{}
	current := time.Unix(1_700_000_000, 0).UTC()
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return current
	}
	cache := NewMemoryNonceCache(time.Minute, clock)

	if !cache.Claim("a") {
		t.Fatal("first claim must succeed")
	}
	if cache.Claim("a") {
		t.Fatal("replay must be refused")
	}
	if !cache.Claim("b") {
		t.Fatal("distinct id must succeed")
	}

	mu.Lock()
	current = current.Add(2 * time.Minute)
	mu.Unlock()
	if !cache.Claim("a") {
		t.Fatal("claim after TTL expiry must succeed again")
	}
}

func TestMemoryNonceCacheEvictsUnderPressure(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	cache := NewMemoryNonceCache(time.Hour, func() time.Time { return now })
	for i := 0; i < 3000; i++ {
		cache.Claim(string(rune(i%128)) + "-suffix-" + string(rune(i)))
	}
	// Entries from the same instant stay claimed; this only asserts the map
	// stays functional past the eviction threshold.
	if !cache.Claim("after-eviction") {
		t.Fatal("cache stopped accepting after eviction threshold")
	}
}
