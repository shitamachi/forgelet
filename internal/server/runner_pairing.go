package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// A device that is not registered yet shows a short code. An administrator
// approves it in the Web console, and the device then collects its credential
// exactly once. Nothing is typed into a configuration file, and one machine is
// one runner: there is no profile to bind.
const (
	pairingTTL       = 10 * time.Minute
	pairingInterval  = 5 * time.Second
	pairingLimit     = 32
	pairCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
)

type runnerPairing struct {
	code      string
	secret    string
	hostname  string
	os, arch  string
	version   string
	created   time.Time
	expires   time.Time
	approved  bool
	name      string
	token     string
	delivered bool
}

type pairingView struct {
	ID        string    `json:"id"`
	Code      string    `json:"code"`
	Hostname  string    `json:"hostname"`
	OS        string    `json:"os,omitempty"`
	Arch      string    `json:"arch,omitempty"`
	Version   string    `json:"version,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *Server) prunePairingsLocked(now time.Time) {
	for id, pairing := range s.pairings {
		if now.After(pairing.expires) {
			delete(s.pairings, id)
		}
	}
	for address, seen := range s.pairSeen {
		if now.Sub(seen) > time.Minute {
			delete(s.pairSeen, address)
		}
	}
}

func pairingCode() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	code := make([]byte, 0, 9)
	for i, value := range raw {
		if i == 4 {
			code = append(code, '-')
		}
		code = append(code, pairCodeAlphabet[int(value)%len(pairCodeAlphabet)])
	}
	return string(code), nil
}

func pairingSecret() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

// runnerPair creates a pending pairing. It is unauthenticated by design, so it
// is bounded: one request per address every few seconds and a small pool.
func (s *Server) runnerPair(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Hostname string `json:"hostname"`
		OS       string `json:"os"`
		Arch     string `json:"arch"`
		Version  string `json:"version"`
	}
	if decode(w, r, &input, 4096) != nil {
		http.Error(w, "invalid pairing request", http.StatusBadRequest)
		return
	}
	now := s.now().UTC()
	remote := remoteHost(r)
	s.runnerMu.Lock()
	defer s.runnerMu.Unlock()
	s.prunePairingsLocked(now)
	if len(s.pairings) >= pairingLimit {
		http.Error(w, "too many pending devices", http.StatusTooManyRequests)
		return
	}
	if last, ok := s.pairSeen[remote]; ok && now.Sub(last) < s.pairCooldown() {
		http.Error(w, "pairing too frequent", http.StatusTooManyRequests)
		return
	}
	code, err := pairingCode()
	if err != nil {
		serverError(w, err)
		return
	}
	secret, err := pairingSecret()
	if err != nil {
		serverError(w, err)
		return
	}
	id, err := randomID()
	if err != nil {
		serverError(w, err)
		return
	}
	pairing := &runnerPairing{code: code, secret: secret, hostname: sanitizeHostname(input.Hostname),
		os: truncate(input.OS, 32), arch: truncate(input.Arch, 32), version: truncate(input.Version, 64),
		created: now, expires: now.Add(pairingTTL)}
	s.pairings[id] = pairing
	s.pairSeen[remote] = now
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "code": code, "secret": secret, "expires_at": pairing.expires,
	})
}

// runnerPairStatus is polled by the device. It returns its credential once,
// after an administrator approved the code.
func (s *Server) runnerPairStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "pairingID")
	secret := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	now := s.now().UTC()
	s.runnerMu.Lock()
	defer s.runnerMu.Unlock()
	s.prunePairingsLocked(now)
	pairing, ok := s.pairings[id]
	if !ok || subtle.ConstantTimeCompare([]byte(secret), []byte(pairing.secret)) != 1 {
		http.Error(w, "unknown or expired pairing", http.StatusNotFound)
		return
	}
	if !pairing.approved {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"state":"pending"}`))
		return
	}
	if pairing.delivered {
		http.Error(w, "pairing already delivered", http.StatusConflict)
		return
	}
	pairing.delivered = true
	writeJSON(w, http.StatusOK, map[string]string{"state": "approved", "name": pairing.name, "token": pairing.token})
}

// listPairings shows the codes waiting for approval.
func (s *Server) listPairings(w http.ResponseWriter, r *http.Request) {
	now := s.now().UTC()
	s.runnerMu.Lock()
	defer s.runnerMu.Unlock()
	s.prunePairingsLocked(now)
	views := make([]pairingView, 0, len(s.pairings))
	for id, pairing := range s.pairings {
		if pairing.approved {
			continue
		}
		views = append(views, pairingView{ID: id, Code: pairing.code, Hostname: pairing.hostname,
			OS: pairing.os, Arch: pairing.arch, Version: pairing.version,
			CreatedAt: pairing.created, ExpiresAt: pairing.expires})
	}
	slices.SortFunc(views, func(a, b pairingView) int { return a.CreatedAt.Compare(b.CreatedAt) })
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, views)
}

// approvePairing names the device and prepares its credential. The device
// collects it on its next poll; an uncollected one expires with the pairing.
func (s *Server) approvePairing(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name string `json:"name"`
	}
	if decode(w, r, &input, 4096) != nil {
		http.Error(w, "invalid approval", http.StatusBadRequest)
		return
	}
	id := chi.URLParam(r, "pairingID")
	now := s.now().UTC()
	s.runnerMu.Lock()
	defer s.runnerMu.Unlock()
	s.prunePairingsLocked(now)
	pairing, ok := s.pairings[id]
	if !ok {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = sanitizeHostname(pairing.hostname)
	}
	if !runnerName.MatchString(name) {
		http.Error(w, "invalid runner name", http.StatusUnprocessableEntity)
		return
	}
	token, digest := runnerToken()
	if entry, exists := s.runners[name]; exists {
		// Re-pairing an existing machine replaces a credential only while that
		// device is offline and idle: a code proves possession of the machine,
		// but a running device is never taken over silently.
		if s.deviceActiveLocked(r.Context(), name, now) {
			http.Error(w, "runner name already exists and is online or running a job", http.StatusConflict)
			return
		}
		entry.Digest = digest
		if err := s.saveRunnerLocked(r.Context(), name, entry); err != nil {
			serverError(w, err)
			return
		}
	} else {
		if len(s.runners) >= 128 {
			http.Error(w, "runner registration limit reached", http.StatusUnprocessableEntity)
			return
		}
		if err := s.saveRunnerLocked(r.Context(), name, runnerRegistration{Digest: digest}); err != nil {
			serverError(w, err)
			return
		}
	}
	pairing.approved, pairing.name, pairing.token = true, name, token
	pairing.expires = now.Add(pairingTTL)
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "status": "approved"})
}

// deviceActiveLocked reports whether a device polled recently or holds a lease.
func (s *Server) deviceActiveLocked(ctx context.Context, name string, now time.Time) bool {
	if seen := s.runnerSeen[name]; !seen.IsZero() && now.Sub(seen) < 30*time.Second {
		return true
	}
	active, err := s.store.Active(ctx)
	if err != nil {
		return true // Refuse the takeover when the state cannot be checked.
	}
	for _, job := range active {
		if job.Plan.External && job.ExternalRunnerID() == name && now.Before(job.LeaseUntil) {
			return true
		}
	}
	return false
}

// rejectPairing forgets a pending code.
func (s *Server) rejectPairing(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "pairingID")
	s.runnerMu.Lock()
	defer s.runnerMu.Unlock()
	if _, ok := s.pairings[id]; !ok {
		http.NotFound(w, r)
		return
	}
	delete(s.pairings, id)
	w.WriteHeader(http.StatusNoContent)
}

func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func sanitizeHostname(hostname string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(hostname)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out.WriteRune(r)
		case r == '-' || r == '.' || r == '_':
			out.WriteRune('-')
		}
		if out.Len() == 48 {
			break
		}
	}
	name := strings.Trim(out.String(), "-")
	if name == "" {
		return "device"
	}
	return name
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func (s *Server) pairingRoutes(r chi.Router) {
	r.Post("/pair", s.runnerPair)
	r.Get("/pair/{pairingID}", s.runnerPairStatus)
}

// pairCooldown bounds how often one address may ask for a code. Tests disable
// it because they pair many devices from one address.
func (s *Server) pairCooldown() time.Duration {
	switch {
	case s.pairInterval > 0:
		return s.pairInterval
	case s.pairInterval < 0:
		return 0
	}
	return pairingInterval
}
