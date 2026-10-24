package github

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type OAuth struct {
	api                        *Client
	web                        *url.URL
	clientID, secret, callback string
}
type OAuthToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	Scope        string    `json:"scope"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
}
type OAuthUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}
type OAuthRepository struct {
	Repository
	Owner struct {
		Login string `json:"login"`
	} `json:"owner"`
	Permissions struct {
		Admin bool `json:"admin"`
	} `json:"permissions"`
}

func NewOAuth(apiURL, webURL, clientID, secret, callback string, client *http.Client) (*OAuth, error) {
	if apiURL == "" {
		apiURL = "https://api.github.com"
	}
	if webURL == "" {
		webURL = "https://github.com"
	}
	parse := func(raw string) (*url.URL, error) {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && (u.Scheme != "http" || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1"))) {
			return nil, errors.New("OAuth endpoints require HTTPS (loopback HTTP only for development)")
		}
		return u, nil
	}
	api, err := parse(apiURL)
	if err != nil {
		return nil, err
	}
	web, err := parse(webURL)
	if err != nil {
		return nil, err
	}
	if _, err = parse(callback); err != nil {
		return nil, err
	}
	if clientID == "" || secret == "" {
		return nil, errors.New("GitHub OAuth client credentials required")
	}
	hc := &http.Client{Timeout: 20 * time.Second}
	if client != nil {
		copy := *client
		hc = &copy
		if hc.Timeout == 0 {
			hc.Timeout = 20 * time.Second
		}
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &OAuth{api: &Client{baseURL: api, http: hc}, web: web, clientID: clientID, secret: secret, callback: callback}, nil
}
func (o *OAuth) AuthorizeURL(state, verifier string) string {
	u := *o.web
	u.Path = strings.TrimRight(u.Path, "/") + "/login/oauth/authorize"
	sum := sha256.Sum256([]byte(verifier))
	u.RawQuery = url.Values{"client_id": {o.clientID}, "redirect_uri": {o.callback}, "scope": {"read:user repo"}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}, "allow_signup": {"false"}}.Encode()
	return u.String()
}
func (o *OAuth) token(ctx context.Context, body map[string]string, now time.Time) (OAuthToken, error) {
	body["client_id"] = o.clientID
	body["client_secret"] = o.secret
	var response struct {
		OAuthToken
		TokenType string `json:"token_type"`
		Expires   int64  `json:"expires_in"`
		Error     string `json:"error"`
	}
	client := &Client{baseURL: o.web, http: o.api.http}
	if err := client.request(ctx, http.MethodPost, "/login/oauth/access_token", nil, "", body, &response); err != nil {
		return OAuthToken{}, fmt.Errorf("GitHub OAuth exchange: %w", err)
	}
	if response.Error != "" || response.AccessToken == "" || !strings.EqualFold(response.TokenType, "bearer") {
		return OAuthToken{}, errors.New("GitHub OAuth exchange rejected; authorize again")
	}
	if response.Expires > 0 {
		if response.Expires > 366*86400 {
			return OAuthToken{}, errors.New("invalid OAuth token lifetime")
		}
		response.ExpiresAt = now.Add(time.Duration(response.Expires) * time.Second)
	}
	scopes := strings.Fields(strings.ReplaceAll(response.Scope, ",", " "))
	hasRepo := false
	for _, scope := range scopes {
		hasRepo = hasRepo || scope == "repo"
	}
	if !hasRepo {
		return OAuthToken{}, errors.New("GitHub authorization did not grant the requested repo scope")
	}
	return response.OAuthToken, nil
}
func (o *OAuth) Exchange(ctx context.Context, code, verifier string, now time.Time) (OAuthToken, error) {
	return o.token(ctx, map[string]string{"code": code, "code_verifier": verifier, "redirect_uri": o.callback}, now)
}
func (o *OAuth) Refresh(ctx context.Context, token OAuthToken, now time.Time) (OAuthToken, error) {
	if token.RefreshToken == "" {
		return OAuthToken{}, errors.New("GitHub authorization expired; reconnect GitHub")
	}
	return o.token(ctx, map[string]string{"grant_type": "refresh_token", "refresh_token": token.RefreshToken}, now)
}
func (o *OAuth) User(ctx context.Context, token string) (OAuthUser, error) {
	var user OAuthUser
	err := o.api.request(ctx, http.MethodGet, "/user", nil, token, nil, &user)
	if err != nil {
		return user, err
	}
	if user.ID <= 0 || user.Login == "" || strings.ContainsAny(user.Login, "/\\\x00\r\n") {
		return OAuthUser{}, errors.New("invalid GitHub user identity")
	}
	return user, nil
}
func (o *OAuth) Repositories(ctx context.Context, token string) ([]OAuthRepository, error) {
	out := []OAuthRepository{}
	seen := map[int64]bool{}
	for page := 1; ; page++ {
		if page > 100 {
			return nil, errors.New("GitHub user repository listing limit exceeded")
		}
		var batch []OAuthRepository
		if err := o.api.request(ctx, http.MethodGet, "/user/repos", pageQuery(page), token, nil, &batch); err != nil {
			return nil, fmt.Errorf("list GitHub user repositories: %w", err)
		}
		for _, repo := range batch {
			if repo.ID <= 0 || !validRepository(repo.Name) || seen[repo.ID] {
				return nil, errors.New("invalid or changing GitHub repository listing; retry")
			}
			seen[repo.ID] = true
			out = append(out, repo)
		}
		if len(batch) < 100 {
			return out, nil
		}
	}
}
