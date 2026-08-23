// Package identity defines the executor workload identity contract: a
// short-lived, audience-bound token whose claims pin namespace, Pod UID,
// JobRun, allowed scopes and expiry. Verification and authorization are
// separate steps: Verify checks the token itself, the caller then matches
// bindings against the request (see internal/security/policy).
package identity

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/shitamachi/forgelet/internal/run/model"
)

// Audience is the only token audience the forgelet control plane accepts.
const Audience = "forgelet-control-plane"

// Scopes an executor identity may carry. A token gets exactly the scopes of
// the interfaces it may call; anything else must be refused downstream.
const (
	ScopePlanRead    = "plan:read"
	ScopeSecretsRead = "secrets:read"
	ScopeStatusWrite = "status:write"
	// ScopeObservedWrite authorizes observed-phase projection for any
	// JobRun. Only control-plane infrastructure (the controller) holds it;
	// executor tokens never do, so their per-JobRun binding stays exact.
	ScopeObservedWrite = "observed:write"
)

// MaxTTL is the upper bound for executor token lifetime.
const MaxTTL = time.Hour

// Identity is the verified set of token claims.
type Identity struct {
	Audience  string         `json:"aud"`
	Namespace string         `json:"ns"`
	PodUID    string         `json:"podUid"`
	JobRunID  model.JobRunID `json:"jobRunId"`
	Scopes    []string       `json:"scopes"`
	TokenID   string         `json:"jti"`
	IssuedAt  time.Time      `json:"iat"`
	ExpiresAt time.Time      `json:"exp"`
}

// HasScope reports whether the identity carries a scope.
func (id Identity) HasScope(scope string) bool {
	for _, s := range id.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// Issuer mints tokens. Implementations must not log token material.
type Issuer interface {
	Issue(ctx context.Context, id Identity) (string, error)
}

// Verifier validates tokens and returns their claims.
type Verifier interface {
	Verify(ctx context.Context, raw string) (Identity, error)
}

// ErrInvalidToken marks all verification failures; wrap for detail.
var ErrInvalidToken = errors.New("identity: invalid token")

// Clock is the injectable time source.
type Clock func() time.Time

// LocalIssuer signs identities with HMAC-SHA256.
//
// DEV/TEST ONLY: production verifies projected ServiceAccount tokens through
// the TokenReview/JWKS adapter (spec 0003 T5). The local scheme exists so the
// authorization protocol can be built and tested without a cluster.
type LocalIssuer struct {
	key []byte
	now Clock
	rng func() (string, error) // token id source
}

// NewLocalIssuer creates a dev/test issuer. key must be at least 32 bytes.
func NewLocalIssuer(key []byte, now Clock) *LocalIssuer {
	if now == nil {
		now = time.Now
	}
	return &LocalIssuer{
		key: append([]byte(nil), key...),
		now: now,
		rng: func() (string, error) {
			var b [16]byte
			if _, err := rand.Read(b[:]); err != nil {
				return "", fmt.Errorf("identity: token id: %w", err)
			}
			return base64.RawURLEncoding.EncodeToString(b[:]), nil
		},
	}
}

// Issue validates the claims and returns a signed token.
func (i *LocalIssuer) Issue(_ context.Context, id Identity) (string, error) {
	if id.Audience != Audience {
		return "", fmt.Errorf("%w: audience must be %q", ErrInvalidToken, Audience)
	}
	if id.Namespace == "" || id.PodUID == "" || id.JobRunID == "" {
		return "", fmt.Errorf("%w: namespace, pod uid and job run id are required bindings", ErrInvalidToken)
	}
	if id.ExpiresAt.IsZero() || id.ExpiresAt.After(i.now().Add(MaxTTL)) {
		return "", fmt.Errorf("%w: expiry required and must be within %s of now", ErrInvalidToken, MaxTTL)
	}
	if id.IssuedAt.IsZero() {
		id.IssuedAt = i.now()
	}
	if id.TokenID == "" {
		tid, err := i.rng()
		if err != nil {
			return "", err
		}
		id.TokenID = tid
	}
	payload, err := json.Marshal(id)
	if err != nil {
		return "", fmt.Errorf("identity: encode claims: %w", err)
	}
	mac := hmac.New(sha256.New, i.key)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// LocalVerifier verifies LocalIssuer tokens.
type LocalVerifier struct {
	key     []byte
	now     Clock
	maxSkew time.Duration
}

// NewLocalVerifier creates a dev/test verifier. maxSkew bounds future iat.
func NewLocalVerifier(key []byte, now Clock, maxSkew time.Duration) *LocalVerifier {
	if now == nil {
		now = time.Now
	}
	return &LocalVerifier{key: append([]byte(nil), key...), now: now, maxSkew: maxSkew}
}

// Verify implements Verifier: signature, expiry, issue time and audience.
func (v *LocalVerifier) Verify(_ context.Context, raw string) (Identity, error) {
	payload, sig, err := splitToken(raw)
	if err != nil {
		return Identity{}, err
	}
	mac := hmac.New(sha256.New, v.key)
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return Identity{}, fmt.Errorf("%w: bad signature", ErrInvalidToken)
	}
	var id Identity
	if err := json.Unmarshal(payload, &id); err != nil {
		return Identity{}, fmt.Errorf("%w: malformed claims: %w", ErrInvalidToken, err)
	}
	if id.Audience != Audience {
		return Identity{}, fmt.Errorf("%w: wrong audience", ErrInvalidToken)
	}
	now := v.now()
	if !id.ExpiresAt.After(now) {
		return Identity{}, fmt.Errorf("%w: token expired", ErrInvalidToken)
	}
	if id.IssuedAt.After(now.Add(v.maxSkew)) {
		return Identity{}, fmt.Errorf("%w: issued in the future", ErrInvalidToken)
	}
	return id, nil
}

func splitToken(raw string) ([]byte, []byte, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return nil, nil, fmt.Errorf("%w: malformed token", ErrInvalidToken)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, nil, fmt.Errorf("%w: payload decode: %w", ErrInvalidToken, err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, nil, fmt.Errorf("%w: signature decode: %w", ErrInvalidToken, err)
	}
	return payload, sig, nil
}

// NonceCache prevents token replay across state-changing calls. Claim returns
// true the first time a token id is seen within its TTL and false afterwards.
type NonceCache interface {
	Claim(tokenID string) bool
}

// MemoryNonceCache is an in-memory NonceCache with TTL eviction.
type MemoryNonceCache struct {
	mu   sync.Mutex
	seen map[string]time.Time
	ttl  time.Duration
	now  Clock
}

// NewMemoryNonceCache returns a cache forgetting entries after ttl.
func NewMemoryNonceCache(ttl time.Duration, now Clock) *MemoryNonceCache {
	if now == nil {
		now = time.Now
	}
	return &MemoryNonceCache{seen: map[string]time.Time{}, ttl: ttl, now: now}
}

// Claim implements NonceCache.
func (c *MemoryNonceCache) Claim(tokenID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if exp, ok := c.seen[tokenID]; ok && now.Before(exp) {
		return false
	}
	// Opportunistic eviction keeps the map bounded without a background loop.
	if len(c.seen) > 1024 {
		for id, exp := range c.seen {
			if !now.Before(exp) {
				delete(c.seen, id)
			}
		}
	}
	c.seen[tokenID] = now.Add(c.ttl)
	return true
}
