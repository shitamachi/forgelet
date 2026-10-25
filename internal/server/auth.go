package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	gh "github.com/shitamachi/forgelet/internal/provider/github"
	"github.com/shitamachi/forgelet/internal/storage"
)

type AuthConfig struct {
	BootstrapPasswordFile string
	GitHub                *gh.OAuth
}
type authStore interface {
	AuthAdmin(context.Context) (storage.AuthUser, error)
	AuthInitialize(context.Context, []byte) error
	AuthPassword(context.Context, []byte, []byte) error
	AuthGitHub(context.Context, int64, string, []byte) error
	AuthSession(context.Context, string, time.Time) (storage.AuthUser, error)
	AuthNewSession(context.Context, string, time.Time, time.Time) error
	AuthLogout(context.Context, string) error
	AuthNewFlow(context.Context, string, []byte, time.Time, time.Time) error
	AuthConsumeFlow(context.Context, string, time.Time) ([]byte, error)
}
type authRuntime struct {
	store    authStore
	key      []byte
	mu       sync.Mutex
	attempts map[string][]time.Time
}

const sessionCookie = "forgelet_session"
const flowCookie = "forgelet_oauth"

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate authentication token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func tokenHash(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
func privateFile(path string, generate func() ([]byte, error)) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		info, e := os.Lstat(path)
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return nil, errors.New("authentication bootstrap files must be regular files with mode 0600")
		}
		return os.ReadFile(path)
	}
	if err != nil {
		return nil, fmt.Errorf("create authentication bootstrap file: %w", err)
	}
	data, err := generate()
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("write authentication bootstrap file: %w", err)
	}
	return data, nil
}

// InitAuth is explicit startup I/O; subsequent starts never reset the administrator.
func (s *Server) InitAuth(ctx context.Context) error {
	if s.cfg.AdminAccess != "session" {
		return nil
	}
	store, ok := s.store.(authStore)
	if !ok {
		return errors.New("authentication storage unavailable")
	}
	key := s.cfg.SecretKey
	if len(key) == 0 {
		admin, adminErr := store.AuthAdmin(ctx)
		if adminErr != nil && !errors.Is(adminErr, storage.ErrNotFound) {
			return fmt.Errorf("read administrator for encryption key: %w", adminErr)
		}
		if len(admin.GitHubToken) > 0 {
			if _, err := os.Lstat(filepath.Join(s.cfg.DataDir, "auth-encryption.key")); err != nil {
				return fmt.Errorf("existing GitHub credentials require the original auth-encryption.key: %w", err)
			}
		}
		var err error
		key, err = privateFile(filepath.Join(s.cfg.DataDir, "auth-encryption.key"), func() ([]byte, error) { b := make([]byte, 32); _, err := rand.Read(b); return b, err })
		if err != nil {
			return err
		}
	}
	if len(key) != 32 {
		return errors.New("authentication encryption key must have 32 bytes")
	}
	if _, err := store.AuthAdmin(ctx); errors.Is(err, storage.ErrNotFound) {
		var password []byte
		if s.cfg.Auth.BootstrapPasswordFile != "" {
			password, err = os.ReadFile(s.cfg.Auth.BootstrapPasswordFile)
		} else {
			password, err = privateFile(filepath.Join(s.cfg.DataDir, "initial-admin-password"), func() ([]byte, error) { token, err := randomToken(); return []byte(token + "\n"), err })
		}
		if err != nil {
			return fmt.Errorf("read initial administrator password: %w", err)
		}
		value := strings.TrimSpace(string(password))
		if !validPassword(value) {
			return errors.New("initial admin password must be 16 to 72 bytes")
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(value), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("hash initial administrator password: %w", err)
		}
		if err = store.AuthInitialize(ctx, hash); err != nil {
			return fmt.Errorf("initialize administrator: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("load administrator: %w", err)
	}
	admin, err := store.AuthAdmin(ctx)
	if err != nil {
		return err
	}
	if len(admin.GitHubToken) > 0 {
		if _, err := openSecret(key, "auth", "github-token", admin.GitHubToken); err != nil {
			return errors.New("cannot decrypt existing GitHub credentials; restore the original encryption key")
		}
	}
	s.auth = &authRuntime{store: store, key: append([]byte(nil), key...), attempts: map[string][]time.Time{}}
	return nil
}
func validPassword(s string) bool { return len(s) >= 16 && len(s) <= 72 && !strings.ContainsRune(s, 0) }
func (s *Server) user(r *http.Request) (storage.AuthUser, error) {
	if s.auth == nil {
		return storage.AuthUser{}, storage.ErrNotFound
	}
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || len(cookie.Value) != 43 {
		return storage.AuthUser{}, storage.ErrNotFound
	}
	return s.auth.store.AuthSession(r.Context(), tokenHash(cookie.Value), s.now())
}
func (s *Server) cookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(s.cfg.PublicURL, "https://"), SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}
func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	result := map[string]any{"mode": s.cfg.AdminAccess, "github_enabled": s.cfg.AdminAccess == "session" && s.cfg.Auth.GitHub != nil, "user": nil}
	if user, err := s.user(r); err == nil {
		result["user"] = user
	}
	writeJSON(w, 200, result)
}
func (s *Server) newSession(w http.ResponseWriter, r *http.Request) error {
	token, err := randomToken()
	if err != nil {
		return err
	}
	if err = s.auth.store.AuthNewSession(r.Context(), tokenHash(token), s.now(), s.now().Add(7*24*time.Hour)); err != nil {
		return err
	}
	s.cookie(w, sessionCookie, token, 7*24*3600)
	return nil
}
func (s *Server) allowLogin(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()
	now := s.now()
	for key, entries := range s.auth.attempts {
		if len(entries) == 0 || now.Sub(entries[len(entries)-1]) >= time.Minute {
			delete(s.auth.attempts, key)
		}
	}
	if len(s.auth.attempts) >= 1024 {
		return false
	}
	recent := []time.Time{}
	for _, t := range s.auth.attempts[host] {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= 6 {
		return false
	}
	s.auth.attempts[host] = append(recent, now)
	return true
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.auth == nil {
		http.Error(w, "session authentication is not enabled", http.StatusServiceUnavailable)
		return
	}
	if !s.openManagement(w, r) {
		return
	}
	if !s.allowLogin(r) {
		http.Error(w, "too many login attempts; retry in one minute", http.StatusTooManyRequests)
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(w, r, &input, 4096); err != nil {
		http.Error(w, "invalid login request", http.StatusBadRequest)
		return
	}
	user, err := s.auth.store.AuthAdmin(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	matches := bcrypt.CompareHashAndPassword(user.PasswordHash, []byte(input.Password)) == nil
	if input.Username != user.Username || !matches {
		http.Error(w, "invalid username or password", http.StatusUnauthorized)
		return
	}
	// Rotate any existing session rather than accepting a caller-supplied identifier.
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		if err = s.auth.store.AuthLogout(r.Context(), tokenHash(cookie.Value)); err != nil {
			serverError(w, err)
			return
		}
	}
	if err = s.newSession(w, r); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"mode": s.cfg.AdminAccess, "github_enabled": s.cfg.Auth.GitHub != nil, "user": user})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		http.Error(w, "session authentication is not enabled", http.StatusServiceUnavailable)
		return
	}
	cookie, err := r.Cookie(sessionCookie)
	if err == nil {
		if err = s.auth.store.AuthLogout(r.Context(), tokenHash(cookie.Value)); err != nil {
			serverError(w, err)
			return
		}
	}
	s.cookie(w, sessionCookie, "", -1)
	writeJSON(w, 200, map[string]bool{"logged_out": true})
}
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Current string `json:"current_password"`
		Next    string `json:"new_password"`
	}
	if err := decode(w, r, &input, 4096); err != nil || !validPassword(input.Next) {
		http.Error(w, "new password must be 16 to 72 bytes", http.StatusBadRequest)
		return
	}
	user, err := s.user(r)
	if err != nil {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if bcrypt.CompareHashAndPassword(user.PasswordHash, []byte(input.Current)) != nil {
		http.Error(w, "current password is incorrect", http.StatusUnauthorized)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Next), bcrypt.DefaultCost)
	if err != nil {
		serverError(w, err)
		return
	}
	if err = s.auth.store.AuthPassword(r.Context(), user.PasswordHash, hash); err != nil {
		serverError(w, err)
		return
	}
	if err = s.newSession(w, r); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"changed": true})
}

type oauthFlow struct {
	Verifier    string `json:"verifier"`
	BrowserHash string `json:"browser_hash"`
	SessionHash string `json:"session_hash,omitempty"`
	Link        bool   `json:"link"`
}

func (s *Server) startGitHub(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil || s.cfg.Auth.GitHub == nil {
		http.Error(w, "GitHub OAuth is not configured", http.StatusServiceUnavailable)
		return
	}
	if !s.openManagement(w, r) {
		return
	}
	var input struct {
		Link bool `json:"link"`
	}
	if err := decode(w, r, &input, 4096); err != nil {
		http.Error(w, "invalid OAuth request", http.StatusBadRequest)
		return
	}
	flow := oauthFlow{Link: input.Link}
	if input.Link {
		if _, err := s.user(r); err != nil {
			http.Error(w, "sign in as admin before linking GitHub", http.StatusUnauthorized)
			return
		}
		cookie, _ := r.Cookie(sessionCookie)
		flow.SessionHash = tokenHash(cookie.Value)
	}
	state, err := randomToken()
	if err != nil {
		serverError(w, err)
		return
	}
	browser, err := randomToken()
	if err != nil {
		serverError(w, err)
		return
	}
	flow.Verifier, err = randomToken()
	if err != nil {
		serverError(w, err)
		return
	}
	flow.BrowserHash = tokenHash(browser)
	data, err := json.Marshal(flow)
	if err != nil {
		serverError(w, err)
		return
	}
	sealed, err := sealSecret(s.auth.key, "auth", "flow", string(data))
	if err != nil {
		serverError(w, err)
		return
	}
	if err = s.auth.store.AuthNewFlow(r.Context(), tokenHash(state), sealed, s.now(), s.now().Add(10*time.Minute)); err != nil {
		serverError(w, err)
		return
	}
	s.cookie(w, flowCookie, browser, 600)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]string{"url": s.cfg.Auth.GitHub.AuthorizeURL(state, flow.Verifier)})
}
func (s *Server) githubCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if s.auth == nil || s.cfg.Auth.GitHub == nil {
		oauthCallbackError(w, "GitHub OAuth is not configured", http.StatusServiceUnavailable)
		return
	}
	state := r.URL.Query().Get("state")
	if len(state) != 43 {
		oauthCallbackError(w, "invalid OAuth state", http.StatusBadRequest)
		return
	}
	sealed, err := s.auth.store.AuthConsumeFlow(r.Context(), tokenHash(state), s.now())
	if err != nil {
		oauthCallbackError(w, "OAuth state expired or already used", http.StatusBadRequest)
		return
	}
	plain, err := openSecret(s.auth.key, "auth", "flow", sealed)
	var flow oauthFlow
	if err != nil || json.Unmarshal([]byte(plain), &flow) != nil {
		oauthCallbackError(w, "invalid OAuth state", http.StatusBadRequest)
		return
	}
	browser, err := r.Cookie(flowCookie)
	if err != nil || tokenHash(browser.Value) != flow.BrowserHash {
		oauthCallbackError(w, "OAuth browser does not match", http.StatusBadRequest)
		return
	}
	s.cookie(w, flowCookie, "", -1)
	if flow.Link {
		user, err := s.user(r)
		if err != nil || user.ID != 1 {
			oauthCallbackError(w, "administrator session expired; reconnect GitHub", http.StatusUnauthorized)
			return
		}
		cookie, _ := r.Cookie(sessionCookie)
		if tokenHash(cookie.Value) != flow.SessionHash {
			oauthCallbackError(w, "administrator session changed; reconnect GitHub", http.StatusUnauthorized)
			return
		}
	}
	code := r.URL.Query().Get("code")
	if code == "" || len(code) > 2048 || r.URL.Query().Get("error") != "" {
		oauthCallbackError(w, "GitHub authorization declined; sign in and retry", http.StatusBadRequest)
		return
	}
	token, err := s.cfg.Auth.GitHub.Exchange(r.Context(), code, flow.Verifier, s.now())
	if err != nil {
		oauthCallbackError(w, err.Error(), http.StatusBadGateway)
		return
	}
	identity, err := s.cfg.Auth.GitHub.User(r.Context(), token.AccessToken)
	if err != nil {
		oauthCallbackError(w, "cannot verify GitHub identity", http.StatusBadGateway)
		return
	}
	user, err := s.auth.store.AuthAdmin(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	if (!flow.Link && user.GitHubID != identity.ID) || (user.GitHubID != 0 && user.GitHubID != identity.ID) {
		oauthCallbackError(w, "GitHub account is not linked to the administrator; use the admin password to link it", http.StatusForbidden)
		return
	}
	data, err := json.Marshal(token)
	if err != nil {
		serverError(w, err)
		return
	}
	encrypted, err := sealSecret(s.auth.key, "auth", "github-token", string(data))
	if err != nil {
		serverError(w, err)
		return
	}
	if err = s.auth.store.AuthGitHub(r.Context(), identity.ID, identity.Login, encrypted); err != nil {
		serverError(w, err)
		return
	}
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		if err = s.auth.store.AuthLogout(r.Context(), tokenHash(cookie.Value)); err != nil {
			serverError(w, err)
			return
		}
	}
	if err = s.newSession(w, r); err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, "/connections?github=connected", http.StatusSeeOther)
}
func (s *Server) githubUserToken(ctx context.Context) (gh.OAuthToken, error) {
	if s.auth == nil || s.cfg.Auth.GitHub == nil {
		return gh.OAuthToken{}, errors.New("GitHub OAuth is not configured")
	}
	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()
	user, err := s.auth.store.AuthAdmin(ctx)
	if err != nil {
		return gh.OAuthToken{}, err
	}
	if user.GitHubID == 0 {
		return gh.OAuthToken{}, errors.New("connect GitHub in account settings first")
	}
	plain, err := openSecret(s.auth.key, "auth", "github-token", user.GitHubToken)
	if err != nil {
		return gh.OAuthToken{}, err
	}
	var token gh.OAuthToken
	if err = json.Unmarshal([]byte(plain), &token); err != nil {
		return token, errors.New("invalid stored GitHub connection")
	}
	if !token.ExpiresAt.IsZero() && token.ExpiresAt.Before(s.now().Add(time.Minute)) {
		token, err = s.cfg.Auth.GitHub.Refresh(ctx, token, s.now())
		if err != nil {
			return token, err
		}
		identity, e := s.cfg.Auth.GitHub.User(ctx, token.AccessToken)
		if e != nil || identity.ID != user.GitHubID {
			return token, errors.New("refreshed GitHub identity does not match")
		}
		data, e := json.Marshal(token)
		if e != nil {
			return token, e
		}
		sealed, e := sealSecret(s.auth.key, "auth", "github-token", string(data))
		if e != nil {
			return token, e
		}
		if err = s.auth.store.AuthGitHub(ctx, user.GitHubID, identity.Login, sealed); err != nil {
			return token, err
		}
	}
	return token, nil
}

func oauthCallbackError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; frame-ancestors 'none'")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><title>GitHub 登录未完成</title><h1>GitHub 登录未完成</h1><p>%s</p><p><a href="/">返回 forgelet 重试</a></p></html>`, html.EscapeString(message))
}
