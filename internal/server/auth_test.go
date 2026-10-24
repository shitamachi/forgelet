package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gh "github.com/shitamachi/forgelet/internal/provider/github"
)

type authHarness struct {
	*harness
	client   *http.Client
	password string
}

func sessionHarness(t *testing.T) *authHarness {
	t.Helper()
	h := newHarness(t, 2)
	h.http.Close()
	h.cfg.AdminAccess = "session"
	h.cfg.PublicURL = "http://localhost"
	h.start()
	password, err := os.ReadFile(filepath.Join(h.cfg.DataDir, "initial-admin-password"))
	if err != nil {
		t.Fatal(err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &authHarness{harness: h, password: strings.TrimSpace(string(password)), client: &http.Client{Jar: jar, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (h *authHarness) call(method, path string, body any, headers map[string]string) (int, []byte, http.Header) {
	h.t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		h.t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(h.t.Context(), method, h.http.URL+path, bytes.NewReader(data))
	if err != nil {
		h.t.Fatal(err)
	}
	if method != "GET" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	response, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	data, err = io.ReadAll(response.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	return response.StatusCode, data, response.Header
}
func (h *authHarness) signIn() {
	h.t.Helper()
	if code, body, _ := h.call("POST", "/api/auth/login", map[string]string{"username": "admin", "password": h.password}, nil); code != 200 || !bytes.Contains(body, []byte(`"username":"admin"`)) {
		h.t.Fatalf("login %d %s", code, body)
	}
}
func TestSessionAuthenticationBootstrapRestartAndRevocation(t *testing.T) {
	h := sessionHarness(t)
	if code, _, _ := h.call("GET", "/api/runs", nil, nil); code != 401 {
		t.Fatal(code)
	}
	if code, _, _ := h.call("GET", "/api/config", nil, map[string]string{"Authorization": "Bearer " + testAdmin}); code != 401 {
		t.Fatal("legacy token bypass", code)
	}
	code, _, headers := h.call("POST", "/api/auth/login", map[string]string{"username": "admin", "password": h.password}, nil)
	if code != 200 {
		t.Fatal(code)
	}
	cookie := headers.Get("Set-Cookie")
	if !strings.Contains(cookie, "HttpOnly") || !strings.Contains(cookie, "SameSite=Lax") {
		t.Fatal(cookie)
	}
	if code, _, _ := h.call("GET", "/api/config", nil, nil); code != 200 {
		t.Fatal(code)
	}
	if code, _, _ := h.call("POST", "/api/backup", map[string]any{}, map[string]string{"Origin": "https://evil.invalid"}); code != 403 {
		t.Fatal("CSRF accepted", code)
	}
	oldURL, _ := url.Parse(h.http.URL)
	oldCookies := h.client.Jar.Cookies(oldURL)
	h.http.Close()
	h.start()
	newURL, _ := url.Parse(h.http.URL)
	h.client.Jar.SetCookies(newURL, oldCookies)
	if code, _, _ := h.call("GET", "/api/config", nil, nil); code != 200 {
		t.Fatal("session lost after restart", code)
	}
	next := "changed-test-password-12345"
	if code, body, _ := h.call("POST", "/api/auth/password", map[string]string{"current_password": h.password, "new_password": next}, nil); code != 200 {
		t.Fatalf("password %d %s", code, body)
	}
	h.password = next
	other := *h.client
	jar, _ := cookiejar.New(nil)
	other.Jar = jar
	other.Jar.SetCookies(newURL, oldCookies)
	request, _ := http.NewRequestWithContext(t.Context(), "GET", h.http.URL+"/api/config", nil)
	response, err := other.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("old session retained", response.StatusCode)
	}
	if code, _, _ := h.call("POST", "/api/auth/logout", map[string]any{}, nil); code != 200 {
		t.Fatal(code)
	}
	if code, _, _ := h.call("GET", "/api/config", nil, nil); code != 401 {
		t.Fatal(code)
	}
	h.signIn()
	info, err := os.Stat(filepath.Join(h.cfg.DataDir, "initial-admin-password"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
}
func TestLoginRateLimitAndExpiry(t *testing.T) {
	h := sessionHarness(t)
	now := h.server.now()
	h.server.now = func() time.Time { return now }
	for i := 0; i < 6; i++ {
		if code, _, _ := h.call("POST", "/api/auth/login", map[string]string{"username": "nobody", "password": "wrong"}, nil); code != 401 {
			t.Fatal(code)
		}
	}
	if code, _, _ := h.call("POST", "/api/auth/login", map[string]string{"username": "admin", "password": h.password}, nil); code != 429 {
		t.Fatal(code)
	}
	now = now.Add(2 * time.Minute)
	h.signIn()
	now = now.Add(8 * 24 * time.Hour)
	if code, _, _ := h.call("GET", "/api/config", nil, nil); code != 401 {
		t.Fatal("expired session accepted", code)
	}
}
func oauthFixture(t *testing.T, h *authHarness) (*onboardingFake, *string) {
	t.Helper()
	challenge := new(string)
	catalog := gh.Catalog{Repositories: []gh.Repository{}, Installations: []gh.Installation{{ID: 42, Permissions: map[string]string{"contents": "read", "checks": "write"}}}}
	catalog.Installations[0].Account.Login = "owner"
	catalog.Installations[0].ManageURL = "https://github.com/settings/installations/42"
	catalog.InstallURL = "https://github.com/apps/test/installations/new"
	p := &onboardingFake{fakeProvider: h.provider, catalog: catalog, sha: testSHA}
	h.server.provider = p
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/login/oauth/access_token":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			sum := sha256.Sum256([]byte(body["code_verifier"]))
			if body["client_secret"] != "test-secret" || base64.RawURLEncoding.EncodeToString(sum[:]) != *challenge {
				http.Error(w, "bad verifier", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "test-oauth-secret", "scope": "repo,read:user", "token_type": "bearer"})
		case "/user":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 789, "login": "owner"})
		case "/user/repos":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 123, "full_name": "owner/project", "default_branch": "main", "owner": map[string]string{"login": "owner"}, "permissions": map[string]bool{"admin": true}}})
		case "/user/installations/42/repositories/123":
			t.Error("OAuth App token must never mutate GitHub App installation access")
			http.Error(w, "OAuth Apps cannot access this endpoint", http.StatusForbidden)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)
	oauth, err := gh.NewOAuth(api.URL, api.URL, "test-client", "test-secret", h.http.URL+"/api/auth/github/callback", api.Client())
	if err != nil {
		t.Fatal(err)
	}
	h.server.cfg.Auth.GitHub = oauth
	return p, challenge
}
func (h *authHarness) startOAuth(link bool, challenge *string) string {
	h.t.Helper()
	code, data, _ := h.call("POST", "/api/auth/github/start", map[string]bool{"link": link}, nil)
	if code != 200 {
		h.t.Fatalf("start %d %s", code, data)
	}
	var response map[string]string
	_ = json.Unmarshal(data, &response)
	u, err := url.Parse(response["url"])
	if err != nil {
		h.t.Fatal(err)
	}
	*challenge = u.Query().Get("code_challenge")
	return u.Query().Get("state")
}
func TestOAuthLoginBindingStateReplayAndRepositoryConsent(t *testing.T) {
	h := sessionHarness(t)
	provider, challenge := oauthFixture(t, h)
	state := h.startOAuth(false, challenge)
	if code, _, _ := h.call("GET", "/api/auth/github/callback?code=valid&state="+state, nil, nil); code != 403 {
		t.Fatal("unlinked user logged in", code)
	}
	h.signIn()
	state = h.startOAuth(true, challenge)
	code, body, headers := h.call("GET", "/api/auth/github/callback?code=valid&state="+state, nil, nil)
	if code != 303 || headers.Get("Location") != "/connections?github=connected" {
		t.Fatalf("callback %d %s", code, body)
	}
	if code, _, _ := h.call("GET", "/api/auth/github/callback?code=valid&state="+state, nil, nil); code != 400 {
		t.Fatal("state replay", code)
	}
	user, err := h.store.AuthAdmin(t.Context())
	if err != nil || user.GitHubID != 789 || bytes.Contains(user.GitHubToken, []byte("test-oauth-secret")) {
		t.Fatal(user, err)
	}
	if code, body, _ := h.call("GET", "/api/github/user/repositories", nil, nil); code != 200 || !bytes.Contains(body, []byte("owner/project")) || bytes.Contains(body, []byte("test-oauth-secret")) {
		t.Fatalf("repos %d %s", code, body)
	}
	before := h.server.management()
	if code, body, _ := h.call("POST", "/api/github/user/enable", onboardingInput{RepositoryID: 123}, nil); code != 202 || !bytes.Contains(body, []byte(`"status":"authorization_required"`)) || !bytes.Contains(body, []byte(`"reason":"repository_not_granted"`)) {
		t.Fatalf("authorization step %d %s", code, body)
	}
	if h.server.allowed("owner/project") || h.server.management().Version != before.Version {
		t.Fatal("pending authorization changed trusted repository configuration")
	}
	// Simulate the user's separate GitHub consent, then re-read App authority.
	provider.catalog.Repositories = []gh.Repository{{ID: 123, Name: "owner/project", InstallationID: 42, DefaultBranch: "main"}}
	if code, body, _ := h.call("POST", "/api/github/user/enable", onboardingInput{RepositoryID: 123}, nil); code != 200 {
		t.Fatalf("enable %d %s", code, body)
	}
	if !h.server.allowed("owner/project") {
		t.Fatal("OAuth-selected repository not enabled")
	}
	if code, _, _ := h.call("POST", "/api/auth/logout", map[string]any{}, nil); code != 200 {
		t.Fatal(code)
	}
	state = h.startOAuth(false, challenge)
	if code, body, _ := h.call("GET", "/api/auth/github/callback?code=valid&state="+state, nil, nil); code != 303 {
		t.Fatalf("GitHub login %d %s", code, body)
	}
}
func TestOAuthRejectsBrowserMismatchAndLostLinkSession(t *testing.T) {
	for _, mode := range []string{"browser", "session", "declined", "expired"} {
		t.Run(mode, func(t *testing.T) {
			h := sessionHarness(t)
			_, challenge := oauthFixture(t, h)
			h.signIn()
			state := h.startOAuth(true, challenge)
			switch mode {
			case "browser":
				jar, _ := cookiejar.New(nil)
				h.client.Jar = jar
			case "session":
				if code, _, _ := h.call("POST", "/api/auth/logout", map[string]any{}, nil); code != 200 {
					t.Fatal(code)
				}
			case "expired":
				now := h.server.now().Add(11 * time.Minute)
				h.server.now = func() time.Time { return now }
			}
			query := "?code=valid&state=" + state
			if mode == "declined" {
				query = "?error=access_denied&state=" + state
			}
			code, _, _ := h.call("GET", "/api/auth/github/callback"+query, nil, nil)
			want := 400
			if mode == "session" {
				want = 401
			}
			if code != want {
				t.Fatalf("%s: %d", mode, code)
			}
			user, _ := h.store.AuthAdmin(t.Context())
			if user.GitHubID != 0 {
				t.Fatal("failed flow linked account")
			}
		})
	}
}

func TestGitHubLoginUsesImmutableIDInsteadOfAccountName(t *testing.T) {
	for _, id := range []int64{789, 999} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			h := sessionHarness(t)
			_, challenge := oauthFixture(t, h)
			if err := h.store.AuthGitHub(t.Context(), id, "previous-name", []byte("previous-ciphertext")); err != nil {
				t.Fatal(err)
			}
			state := h.startOAuth(false, challenge)
			code, _, _ := h.call("GET", "/api/auth/github/callback?code=valid&state="+state, nil, nil)
			user, err := h.store.AuthAdmin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if id == 789 {
				if code != 303 || user.GitHubLogin != "owner" {
					t.Fatal("matching ID rename rejected", code, user)
				}
			} else {
				if code != 403 || user.GitHubID != 999 || string(user.GitHubToken) != "previous-ciphertext" {
					t.Fatal("different account rebound administrator", code, user)
				}
			}
		})
	}
}

func TestGitHubRefreshPreservesAdministratorIdentity(t *testing.T) {
	for _, match := range []bool{true, false} {
		t.Run(fmt.Sprint(match), func(t *testing.T) {
			h := sessionHarness(t)
			old := gh.OAuthToken{AccessToken: "expired-token", RefreshToken: "old-refresh", Scope: "repo", ExpiresAt: time.Now().Add(-time.Hour)}
			body, err := json.Marshal(old)
			if err != nil {
				t.Fatal(err)
			}
			sealed, err := sealSecret(h.server.auth.key, "auth", "github-token", string(body))
			if err != nil {
				t.Fatal(err)
			}
			if err = h.store.AuthGitHub(t.Context(), 789, "owner", sealed); err != nil {
				t.Fatal(err)
			}
			fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/login/oauth/access_token" {
					var input map[string]string
					_ = json.NewDecoder(r.Body).Decode(&input)
					if input["grant_type"] != "refresh_token" || input["refresh_token"] != "old-refresh" {
						t.Error("refresh request did not use stored grant")
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "renewed-token", "refresh_token": "next-refresh", "scope": "repo,read:user", "token_type": "bearer", "expires_in": 3600})
					return
				}
				id := 789
				if !match {
					id = 999
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "login": "owner"})
			}))
			defer fixture.Close()
			h.server.cfg.Auth.GitHub, err = gh.NewOAuth(fixture.URL, fixture.URL, "client", "secret", h.http.URL+"/api/auth/github/callback", fixture.Client())
			if err != nil {
				t.Fatal(err)
			}
			token, err := h.server.githubUserToken(t.Context())
			user, readErr := h.store.AuthAdmin(t.Context())
			if readErr != nil {
				t.Fatal(readErr)
			}
			if match {
				if err != nil || token.AccessToken != "renewed-token" || token.RefreshToken != "next-refresh" || bytes.Contains(user.GitHubToken, []byte("renewed-token")) {
					t.Fatal("refresh did not rotate encrypted credentials", err)
				}
				again, e := h.server.githubUserToken(t.Context())
				if e != nil || again.AccessToken != "renewed-token" {
					t.Fatal("renewed credential not persisted", e)
				}
			} else if err == nil || !bytes.Equal(user.GitHubToken, sealed) {
				t.Fatal("refresh rebound another GitHub account", err)
			}
		})
	}
}

func TestRepositoryConsentNeverEnablesBeforeAuthoritativeGrant(t *testing.T) {
	for _, mode := range []string{"repository_not_granted", "installation_required", "installation_suspended", "installation_permissions", "stale", "unknown_repository", "wrong_binding", "catalog_failure"} {
		t.Run(mode, func(t *testing.T) {
			h := sessionHarness(t)
			p, challenge := oauthFixture(t, h)
			h.signIn()
			state := h.startOAuth(true, challenge)
			if code, _, _ := h.call("GET", "/api/auth/github/callback?code=valid&state="+state, nil, nil); code != 303 {
				t.Fatal(code)
			}
			input := onboardingInput{RepositoryID: 123}
			expected := 202
			switch mode {
			case "installation_required":
				p.catalog.Installations = nil
			case "installation_suspended":
				now := time.Now()
				p.catalog.Installations[0].SuspendedAt = &now
			case "installation_permissions":
				p.catalog.Installations[0].Permissions = map[string]string{"contents": "read"}
			case "stale":
				input.Version = 99
				expected = 409
			case "unknown_repository":
				input.RepositoryID = 777
				expected = 403
			case "wrong_binding":
				p.catalog.Repositories = []gh.Repository{{ID: 123, Name: "other/project", InstallationID: 42}} // Same ID cannot confer a different name.
			case "catalog_failure":
				p.err = errors.New("remote unavailable")
				expected = 502
			}
			code, body, _ := h.call("POST", "/api/github/user/enable", input, nil)
			if code != expected {
				t.Fatalf("got %d %s, want %d", code, body, expected)
			}
			if expected == 202 {
				var result map[string]any
				if err := json.Unmarshal(body, &result); err != nil {
					t.Fatal(err)
				}
				if result["status"] != "authorization_required" || result["repository"] != "owner/project" || !strings.HasPrefix(result["authorize_url"].(string), "https://github.com/") {
					t.Fatal(result)
				}
				if mode != "wrong_binding" && result["reason"] != mode {
					t.Fatal(result)
				}
			}
			if h.server.allowed("owner/project") || h.server.management().Version != 0 {
				t.Fatal("consent failure altered trusted configuration")
			}
		})
	}
}
