// Package github implements the GitHub App source and Checks adapters.
package github

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxResponseBytes = 2 << 20

type tokenKey struct {
	installation int64
	permission   string
	repository   string
}

type cachedToken struct {
	value string
	until time.Time
}

// Client talks only to its configured API origin. It never follows redirects or
// exposes API response bodies, authorization headers, or private keys in errors.
type Client struct {
	baseURL *url.URL
	appID   int64
	key     *rsa.PrivateKey
	http    *http.Client
	now     func() time.Time
	mu      sync.Mutex
	tokens  map[tokenKey]cachedToken
}

// New accepts the numeric GitHub App ID and a PKCS#1 or PKCS#8 RSA private key.
// An empty baseURL selects GitHub.com. Plain HTTP is restricted to loopback for
// local integration tests; enterprise API base paths are preserved.
func New(baseURL, appID string, privateKeyPEM []byte, client *http.Client) (*Client, error) {
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("github: invalid API base URL")
	}
	loopback := u.Hostname() == "localhost"
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		loopback = ip.IsLoopback()
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !loopback) {
		return nil, errors.New("github: API requires HTTPS")
	}
	id, err := strconv.ParseInt(appID, 10, 64)
	if err != nil || id <= 0 {
		return nil, errors.New("github: App ID must be a positive integer")
	}
	block, rest := pem.Decode(privateKeyPEM)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("github: invalid private key PEM")
	}
	var key *rsa.PrivateKey
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		var parsed any
		parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
		key, _ = parsed.(*rsa.PrivateKey)
	default:
		return nil, errors.New("github: expected RSA private key")
	}
	if err != nil || key == nil || key.N.BitLen() < 2048 {
		return nil, errors.New("github: invalid RSA private key (minimum 2048 bits)")
	}
	if err := key.Validate(); err != nil {
		return nil, errors.New("github: invalid RSA private key")
	}
	httpClient := &http.Client{Timeout: 30 * time.Second}
	if client != nil {
		copyClient := *client
		httpClient = &copyClient
		if httpClient.Timeout == 0 {
			httpClient.Timeout = 30 * time.Second
		}
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: u, appID: id, key: key, http: httpClient, now: time.Now, tokens: make(map[tokenKey]cachedToken)}, nil
}

func (c *Client) jwt() (string, error) {
	now := c.now()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{"iss": strconv.FormatInt(c.appID, 10), "iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix()})
	if err != nil {
		return "", fmt.Errorf("github: encode JWT: %w", err)
	}
	payload := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(payload))
	signature, err := rsa.SignPKCS1v15(rand.Reader, c.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", errors.New("github: sign JWT failed")
	}
	return payload + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// InstallationToken returns a short-lived cached, contents-read-only token.
// Use RepositoryToken when a token is issued to an execution Pod.
func (c *Client) InstallationToken(ctx context.Context, installationID int64) (string, error) {
	return c.installationToken(ctx, tokenKey{installation: installationID, permission: "contents"})
}

// RepositoryToken additionally scopes contents access to one installation repository.
func (c *Client) RepositoryToken(ctx context.Context, installationID int64, repository string) (string, error) {
	if !validRepository(repository) {
		return "", errors.New("github: invalid repository")
	}
	return c.installationToken(ctx, tokenKey{installation: installationID, permission: "contents", repository: repository})
}

func (c *Client) installationToken(ctx context.Context, key tokenKey) (string, error) {
	if key.installation <= 0 {
		return "", errors.New("github: invalid installation ID")
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("github: token request: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if cached, ok := c.tokens[key]; ok && c.now().Before(cached.until) {
		return cached.value, nil
	}
	jwt, err := c.jwt()
	if err != nil {
		return "", err
	}
	level := "read"
	if key.permission == "checks" {
		level = "write"
	}
	body := map[string]any{"permissions": map[string]string{key.permission: level}}
	if key.permission == "pr-merge" {
		body["permissions"] = map[string]string{"contents": "read", "pull_requests": "read"}
	}
	if key.permission == "gitops" {
		body["permissions"] = map[string]string{"contents": "write", "pull_requests": "write"}
	}
	if key.repository != "" {
		body["repositories"] = []string{strings.SplitN(key.repository, "/", 2)[1]}
	}
	var response struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	endpoint := "/app/installations/" + strconv.FormatInt(key.installation, 10) + "/access_tokens"
	if err := c.request(ctx, http.MethodPost, endpoint, nil, jwt, body, &response); err != nil {
		return "", fmt.Errorf("github: installation token: %w", err)
	}
	if response.Token == "" || !response.ExpiresAt.After(c.now().Add(time.Minute)) {
		return "", errors.New("github: invalid installation token response")
	}
	until := response.ExpiresAt.Add(-time.Minute)
	if cap := c.now().Add(5 * time.Minute); cap.Before(until) {
		until = cap
	}
	// Bound the cache even when a large number of installations are configured.
	if len(c.tokens) >= 256 {
		clear(c.tokens)
	}
	c.tokens[key] = cachedToken{value: response.Token, until: until}
	return response.Token, nil
}

type apiError struct{ status int }

func (e *apiError) Error() string { return fmt.Sprintf("GitHub API returned HTTP %d", e.status) }

func isStatus(err error, status int) bool {
	var api *apiError
	return errors.As(err, &api) && api.status == status
}

func (c *Client) request(ctx context.Context, method, endpoint string, query url.Values, token string, body, output any) error {
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode GitHub request: %w", err)
		}
	}
	u := *c.baseURL
	u.Path = strings.TrimRight(u.Path, "/") + endpoint
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(encoded))
	if err != nil {
		return errors.New("create GitHub request failed")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	req.Header.Set("User-Agent", "forgelet")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("GitHub request: %w", ctx.Err())
		}
		return errors.New("GitHub request transport failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &apiError{status: response.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return errors.New("read GitHub response failed")
	}
	if len(data) > maxResponseBytes {
		return errors.New("GitHub response exceeds size limit")
	}
	if output != nil {
		if err := json.Unmarshal(data, output); err != nil {
			return errors.New("decode GitHub response failed")
		}
	}
	return nil
}
