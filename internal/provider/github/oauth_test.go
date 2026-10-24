package github

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOAuthPKCEIdentityAndRepos(t *testing.T) {
	verifier := strings.Repeat("v", 43)
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/login/oauth/access_token":
			if r.Header.Get("Authorization") != "" {
				t.Fatal("App credentials sent to OAuth token endpoint")
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["client_id"] != "client" || body["client_secret"] != "secret" || body["code_verifier"] != verifier {
				t.Error(body)
			}
			writeJSON(t, w, map[string]any{"access_token": "user-token", "scope": "repo,read:user", "token_type": "bearer", "expires_in": 3600, "refresh_token": "refresh"})
		case "/user":
			writeJSON(t, w, map[string]any{"id": 123, "login": "owner"})
		case "/user/repos":
			writeJSON(t, w, []map[string]any{{"id": 99, "full_name": "owner/project", "owner": map[string]string{"login": "owner"}, "permissions": map[string]bool{"admin": true}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	oauth, err := NewOAuth(server.URL, server.URL, "client", "secret", server.URL+"/callback", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), "GET", oauth.AuthorizeURL("state", verifier), nil)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(verifier))
	if q := request.URL.Query(); q.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(sum[:]) || q.Get("scope") != "read:user repo" || q.Get("code_challenge_method") != "S256" {
		t.Fatal(q)
	}
	token, err := oauth.Exchange(t.Context(), "code", verifier, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	user, err := oauth.User(t.Context(), token.AccessToken)
	if err != nil || user.ID != 123 {
		t.Fatal(user, err)
	}
	repos, err := oauth.Repositories(t.Context(), token.AccessToken)
	if err != nil || len(repos) != 1 || !repos[0].Permissions.Admin {
		t.Fatal(repos, err)
	}
	if len(calls) != 3 {
		t.Fatal(calls)
	}
}
func TestOAuthRejectsLeakedOrMissingScopeAndRedirects(t *testing.T) {
	for _, mode := range []string{"missing-scope", "error", "redirect", "transport"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "redirect" {
					http.Redirect(w, r, "https://example.invalid/secret", http.StatusFound)
					return
				}
				scope := "repo"
				if mode == "missing-scope" {
					scope = "read:user"
				}
				writeJSON(t, w, map[string]string{"access_token": "must-not-leak", "token_type": "bearer", "scope": scope, "error": map[bool]string{true: "denied"}[mode == "error"]})
			}))
			defer server.Close()
			o, err := NewOAuth(server.URL, server.URL, "client", "secret", server.URL+"/callback", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if mode == "transport" {
				server.Close()
			}
			token, err := o.Exchange(t.Context(), "code", strings.Repeat("v", 43), time.Now())
			if err == nil || token.AccessToken != "" || strings.Contains(err.Error(), "must-not-leak") {
				t.Fatal(token, err)
			}
		})
	}
}
