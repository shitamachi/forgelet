package github

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

func testClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server, *rsa.PrivateKey) {
	t.Helper()
	key, private := testKey(t)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New(server.URL, "42", private, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client, server, key
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func writeToken(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	writeJSON(t, w, map[string]any{"token": "installation-secret", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
}

func TestInstallationTokenJWTAndCache(t *testing.T) {
	var requests atomic.Int32
	var auth string
	client, _, key := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		auth = r.Header.Get("Authorization")
		if r.Method != http.MethodPost || r.URL.Path != "/app/installations/7/access_tokens" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		var body struct {
			Permissions map[string]string `json:"permissions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Permissions) != 1 || body.Permissions["contents"] != "read" {
			t.Errorf("permissions: %#v", body.Permissions)
		}
		writeToken(t, w)
	})
	fixed := time.Now().Truncate(time.Second)
	client.now = func() time.Time { return fixed }
	for range 2 {
		token, err := client.InstallationToken(t.Context(), 7)
		if err != nil || token != "installation-secret" {
			t.Fatalf("token=%q err=%v", token, err)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("cache requests=%d", requests.Load())
	}
	parts := strings.Split(strings.TrimPrefix(auth, "Bearer "), ".")
	if len(parts) != 3 {
		t.Fatal("invalid JWT")
	}
	var header map[string]string
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &header); err != nil {
		t.Fatal(err)
	}
	if header["alg"] != "RS256" {
		t.Fatalf("JWT algorithm=%q", header["alg"])
	}
	var claims struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}
	data, err = base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Iss != "42" || claims.Iat != fixed.Add(-time.Minute).Unix() || claims.Exp != fixed.Add(9*time.Minute).Unix() {
		t.Fatalf("JWT claims=%+v", claims)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Fatal(err)
	}
	client.now = func() time.Time { return fixed.Add(6 * time.Minute) }
	if _, err := client.InstallationToken(t.Context(), 7); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatal("short cache TTL did not refresh")
	}
}

func TestRepositoryTokenScopesAndConcurrentCache(t *testing.T) {
	var requests atomic.Int32
	client, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var body struct {
			Repositories []string          `json:"repositories"`
			Permissions  map[string]string `json:"permissions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Repositories) != 1 || body.Repositories[0] != "repo" || body.Permissions["contents"] != "read" {
			t.Errorf("token scope=%+v", body)
		}
		writeToken(t, w)
	})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := client.RepositoryToken(t.Context(), 7, "owner/repo"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if requests.Load() != 1 {
		t.Fatalf("concurrent token requests=%d", requests.Load())
	}
	if _, err := client.RepositoryToken(t.Context(), 7, "../repo"); err == nil {
		t.Fatal("accepted unsafe repository")
	}
	if _, err := client.InstallationToken(t.Context(), 0); err == nil {
		t.Fatal("accepted zero installation")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.InstallationToken(ctx, 7); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestNewValidation(t *testing.T) {
	key, private := testKey(t)
	for _, tc := range []struct {
		name, base, id string
		key            []byte
	}{
		{"invalid URL", ":bad", "42", private},
		{"missing host", "https:///", "42", private},
		{"userinfo", "https://user:secret@example.com", "42", private},
		{"query", "https://example.com/?secret=x", "42", private},
		{"plain external HTTP", "http://example.com", "42", private},
		{"invalid ID", "", "bad", private},
		{"zero ID", "", "0", private},
		{"invalid PEM", "", "42", []byte("private-secret")},
		{"trailing PEM", "", "42", append(append([]byte(nil), private...), []byte("trailing")...)},
		{"invalid key bytes", "", "42", pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("broken")})},
		{"wrong key type", "", "42", pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte("public")})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.base, tc.id, tc.key, nil); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New("", "42", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), nil); err != nil {
		t.Fatal(err)
	}
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecDER, err := x509.MarshalPKCS8PrivateKey(ec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New("", "42", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecDER}), nil); err == nil {
		t.Fatal("accepted non-RSA PKCS8")
	}
}

func TestAPIErrorsAreBoundedAndRedacted(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"status", http.StatusForbidden, `{"message":"installation-secret"}`},
		{"malformed", http.StatusOK, `installation-secret`},
		{"oversized", http.StatusOK, strings.Repeat("installation-secret", maxResponseBytes)},
		{"empty token", http.StatusOK, `{"token":"","expires_at":"2099-01-01T00:00:00Z"}`},
		{"expired token", http.StatusOK, `{"token":"installation-secret","expires_at":"2000-01-01T00:00:00Z"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); _, _ = fmt.Fprint(w, tc.body) })
			_, err := client.InstallationToken(t.Context(), 7)
			if err == nil || strings.Contains(err.Error(), "installation-secret") {
				t.Fatalf("unsafe error: %v", err)
			}
		})
	}
}

func TestRedirectAndTransportSafety(t *testing.T) {
	var forwarded atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Store(true) }))
	t.Cleanup(target.Close)
	client, server, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	})
	if _, err := client.InstallationToken(t.Context(), 7); err == nil {
		t.Fatal("redirect accepted")
	}
	if forwarded.Load() {
		t.Fatal("credentials forwarded by redirect")
	}
	server.Close()
	if _, err := client.InstallationToken(t.Context(), 7); err == nil || strings.Contains(err.Error(), server.URL) {
		t.Fatalf("transport error: %v", err)
	}
}
