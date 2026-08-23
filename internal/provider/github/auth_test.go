package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func decodeSegment(t *testing.T, seg string) map[string]any {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		t.Fatalf("decode segment: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("parse segment: %v", err)
	}
	return m
}

func TestAppAuthToken(t *testing.T) {
	key := testKey(t)
	var calls int32
	now := time.Unix(1_700_000_000, 0).UTC()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.URL.Path != "/app/installations/42/access_tokens" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		auth := r.Header.Get("Authorization")
		if len(auth) < 8 || auth[:7] != "Bearer " {
			t.Errorf("missing bearer: %q", auth)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// Validate the JWT structure issued by the client.
		parts := splitJWT(auth[7:])
		header := decodeSegment(t, parts[0])
		if header["alg"] != "RS256" || header["typ"] != "JWT" {
			t.Errorf("jwt header = %v", header)
		}
		claims := decodeSegment(t, parts[1])
		if claims["iss"] != float64(7) {
			t.Errorf("iss = %v, want 7", claims["iss"])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"ghs_inst","expires_at":"2023-11-14T22:20:00Z"}`))
	}))
	defer api.Close()

	a := NewAppAuth(7, 42, key, api.URL, api.Client(), func() time.Time { return now })
	ctx := context.Background()

	tok, err := a.Token(ctx)
	if err != nil || tok != "ghs_inst" {
		t.Fatalf("token = %q err = %v", tok, err)
	}
	// Cached: second call does not hit the API.
	if _, err := a.Token(ctx); err != nil {
		t.Fatalf("cached token: %v", err)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("API calls = %d, want 1 (token must be cached)", n)
	}

	// Advance past expiry-minus-margin: refreshes.
	later := func() time.Time { return now.Add(59 * time.Minute) }
	a.Now = later
	if tok, err := a.Token(ctx); err != nil || tok != "ghs_inst" {
		t.Fatalf("refresh: %q %v", tok, err)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("API calls after expiry = %d, want 2", n)
	}
}

func TestAppAuthAPIError(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad credentials", http.StatusUnauthorized)
	}))
	defer api.Close()

	a := NewAppAuth(7, 42, testKey(t), api.URL, api.Client(), nil)
	if _, err := a.Token(context.Background()); err == nil {
		t.Fatal("401 from token endpoint must error")
	}
	// A failed fetch must not cache anything.
	a.mu.Lock()
	cached := a.token
	a.mu.Unlock()
	if cached != "" {
		t.Error("failed token fetch cached a token")
	}
}

func TestAppAuthDefaults(t *testing.T) {
	a := NewAppAuth(1, 2, testKey(t), "", nil, nil)
	if a.BaseURL != DefaultBaseURL || a.HTTP == nil || a.Now == nil {
		t.Errorf("defaults not applied: %+v", a)
	}
}

func splitJWT(jwt string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(jwt); i++ {
		if jwt[i] == '.' {
			parts = append(parts, jwt[start:i])
			start = i + 1
		}
	}
	return append(parts, jwt[start:])
}
