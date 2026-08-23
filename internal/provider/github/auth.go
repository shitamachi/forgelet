package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// TokenSource supplies bearer tokens for GitHub API calls. Adapters other
// than this package never touch JWTs or the App private key (FR-G2.2).
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// DefaultBaseURL is the public GitHub API.
const DefaultBaseURL = "https://api.github.com"

// AppAuth exchanges a signed GitHub App JWT for short-lived installation
// tokens and caches them until shortly before expiry.
type AppAuth struct {
	AppID          int64
	InstallationID int64
	Key            *rsa.PrivateKey
	BaseURL        string
	HTTP           *http.Client
	Now            func() time.Time

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

// NewAppAuth wires an AppAuth with defaults filled in.
func NewAppAuth(appID, installationID int64, key *rsa.PrivateKey, baseURL string, hc *http.Client, now func() time.Time) *AppAuth {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	if now == nil {
		now = time.Now
	}
	return &AppAuth{AppID: appID, InstallationID: installationID, Key: key, BaseURL: baseURL, HTTP: hc, Now: now}
}

// Token implements TokenSource with a cache refreshed one minute early.
func (a *AppAuth) Token(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && a.Now().Before(a.expiresAt.Add(-time.Minute)) {
		return a.token, nil
	}
	token, expires, err := a.installationToken(ctx)
	if err != nil {
		return "", fmt.Errorf("github: installation token: %w", err)
	}
	a.token, a.expiresAt = token, expires
	return token, nil
}

// installationToken signs the App JWT and exchanges it at the API.
func (a *AppAuth) installationToken(ctx context.Context) (string, time.Time, error) {
	jwt, err := a.appJWT()
	if err != nil {
		return "", time.Time{}, err
	}
	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", a.BaseURL, a.InstallationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("github: build token request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := a.HTTP.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("github: token request: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusCreated {
		return "", time.Time{}, fmt.Errorf("github: token endpoint status %d", resp.StatusCode)
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", time.Time{}, fmt.Errorf("github: decode token response: %w", err)
	}
	if out.Token == "" {
		return "", time.Time{}, errors.New("github: empty installation token")
	}
	return out.Token, out.ExpiresAt, nil
}

// appJWT builds the RS256-signed App JWT. GitHub caps exp at 10 minutes;
// forgelet signs for 5 with a 60s issued-at skew.
func (a *AppAuth) appJWT() (string, error) {
	now := a.Now()
	header, err := base64URL(map[string]any{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", fmt.Errorf("github: jwt header: %w", err)
	}
	claims, err := base64URL(map[string]any{
		"iss": a.AppID,
		"iat": now.Add(-time.Minute).Unix(),
		"exp": now.Add(5 * time.Minute).Unix(),
	})
	if err != nil {
		return "", fmt.Errorf("github: jwt claims: %w", err)
	}
	signing := header + "." + claims
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.Key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("github: sign jwt: %w", err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func base64URL(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("github: encode jwt segment: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
