package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/shitamachi/forgelet/internal/run"
)

// A nil policy stores an operator override for a YAML registration.
type runnerRegistration struct {
	Policy *ExternalRunner `json:"policy,omitempty"`
	Digest string          `json:"digest,omitempty"`
	Paused bool            `json:"paused,omitempty"`
}

type runnerView struct {
	Name     string    `json:"name"`
	Source   string    `json:"source"`
	Status   string    `json:"status"`
	Paused   bool      `json:"paused"`
	LastSeen time.Time `json:"last_seen,omitzero"`
	JobID    string    `json:"job_id,omitempty"`
	RunID    string    `json:"run_id,omitempty"`
}

func (s *Server) externalRunner(runnerID string) (ExternalRunner, bool) {
	s.runnerMu.Lock()
	defer s.runnerMu.Unlock()
	return s.externalRunnerLocked(runnerID)
}

func (s *Server) externalRunnerLocked(runnerID string) (ExternalRunner, bool) {
	policy, exists := s.cfg.ExternalRunners[runnerID]
	entry := s.runners[runnerID]
	if entry.Policy != nil {
		policy, exists = *entry.Policy, true
	}
	if entry.Digest != "" {
		policy.TokenSHA256 = entry.Digest
	}
	return policy, exists
}

func (s *Server) runnerPoliciesLocked() map[string]ExternalRunner {
	policies := make(map[string]ExternalRunner, len(s.cfg.ExternalRunners)+len(s.runners))
	for name := range s.cfg.ExternalRunners {
		policies[name], _ = s.externalRunnerLocked(name)
	}
	for name := range s.runners {
		if policy, ok := s.externalRunnerLocked(name); ok {
			policies[name] = policy
		}
	}
	return policies
}

func (s *Server) loadRunners(ctx context.Context) error {
	if s.external == nil {
		return nil
	}
	body, err := s.external.RunnerRegistrations(ctx)
	if err != nil {
		return fmt.Errorf("load runner registrations: %w", err)
	}
	if len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, &s.runners); err != nil {
		return fmt.Errorf("decode runner registrations: %w", err)
	}
	for name, entry := range s.runners {
		if _, configured := s.cfg.ExternalRunners[name]; configured && entry.Policy != nil {
			return fmt.Errorf("runner %q is registered in both SQLite and YAML", name)
		}
	}
	cfg := s.cfg
	cfg.ExternalRunners = s.runnerPoliciesLocked()
	return validateExternalRunners(cfg, s.external)
}

func (s *Server) saveRunnerLocked(ctx context.Context, runnerID string, entry runnerRegistration) error {
	next := make(map[string]runnerRegistration, len(s.runners)+1)
	maps.Copy(next, s.runners)
	next[runnerID] = entry
	body, err := json.Marshal(next)
	if err != nil {
		return fmt.Errorf("encode runner registrations: %w", err)
	}
	if err := s.external.SaveRunnerRegistrations(ctx, body); err != nil {
		return fmt.Errorf("save runner registration: %w", err)
	}
	s.runners = next
	return nil
}

func (s *Server) listRunners(w http.ResponseWriter, r *http.Request) {
	s.runnerMu.Lock()
	defer s.runnerMu.Unlock()
	active, err := s.store.Active(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	now := s.now()
	jobs := make(map[string]run.Job)
	for _, job := range active {
		if job.Plan.External && now.Before(job.LeaseUntil) {
			jobs[job.ExternalRunnerID()] = job
		}
	}
	policies := s.runnerPoliciesLocked()
	views := make([]runnerView, 0, len(policies))
	for _, name := range slices.Sorted(maps.Keys(policies)) {
		source := "web"
		if _, configured := s.cfg.ExternalRunners[name]; configured {
			source = "yaml"
		}
		view := runnerView{Name: name, Source: source,
			LastSeen: s.runnerSeen[name], Paused: s.runners[name].Paused, Status: "offline"}
		switch {
		case jobs[name].ID != "":
			view.Status, view.JobID, view.RunID = "busy", jobs[name].ID, jobs[name].RunID
		case view.Paused:
			view.Status = "paused"
		case !view.LastSeen.IsZero() && now.Sub(view.LastSeen) < 30*time.Second:
			view.Status = "online"
		}
		views = append(views, view)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, views)
}

func runnerToken() (string, string) {
	var nonce [32]byte
	_, _ = rand.Read(nonce[:])
	token := "frr_" + hex.EncodeToString(nonce[:])
	return token, fmt.Sprintf("%x", sha256.Sum256([]byte(token)))
}

func (s *Server) registerRunner(w http.ResponseWriter, r *http.Request) {
	if s.external == nil {
		http.Error(w, "external runners unavailable", http.StatusServiceUnavailable)
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	if err := decode(w, r, &input, 4096); err != nil {
		http.Error(w, "invalid runner registration", http.StatusBadRequest)
		return
	}
	s.runnerMu.Lock()
	defer s.runnerMu.Unlock()
	if _, exists := s.externalRunnerLocked(input.Name); exists {
		http.Error(w, "runner name already exists", http.StatusConflict)
		return
	}
	if len(s.runners) >= 128 {
		http.Error(w, "runner registration limit reached", http.StatusUnprocessableEntity)
		return
	}
	if !runnerName.MatchString(input.Name) {
		http.Error(w, "invalid runner name", http.StatusUnprocessableEntity)
		return
	}
	token, digest := runnerToken()
	if err := s.saveRunnerLocked(r.Context(), input.Name, runnerRegistration{Policy: &ExternalRunner{}, Digest: digest}); err != nil {
		serverError(w, err)
		return
	}
	writeRunnerToken(w, http.StatusCreated, input.Name, token)
}

func (s *Server) pauseRunner(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Paused *bool `json:"paused"`
	}
	if err := decode(w, r, &input, 1024); err != nil || input.Paused == nil {
		http.Error(w, "paused must be a boolean", http.StatusBadRequest)
		return
	}
	runnerID := chi.URLParam(r, "runnerID")
	s.runnerMu.Lock()
	defer s.runnerMu.Unlock()
	if _, exists := s.externalRunnerLocked(runnerID); !exists {
		http.NotFound(w, r)
		return
	}
	entry := s.runners[runnerID]
	entry.Paused = *input.Paused
	err := s.saveRunnerLocked(r.Context(), runnerID, entry)
	if err == nil {
		delete(s.runnerReady, runnerID)
	}
	respond(w, map[string]bool{"paused": entry.Paused}, err)
}

func (s *Server) rotateRunner(w http.ResponseWriter, r *http.Request) {
	runnerID := chi.URLParam(r, "runnerID")
	s.runnerMu.Lock()
	defer s.runnerMu.Unlock()
	if _, exists := s.externalRunnerLocked(runnerID); !exists {
		http.NotFound(w, r)
		return
	}
	active, err := s.store.Active(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	for _, job := range active {
		if job.Plan.External && job.ExternalRunnerID() == runnerID && s.now().Before(job.LeaseUntil) {
			http.Error(w, "wait for the runner's current job to finish", http.StatusConflict)
			return
		}
	}
	token, digest := runnerToken()
	entry := s.runners[runnerID]
	entry.Digest = digest
	if err := s.saveRunnerLocked(r.Context(), runnerID, entry); err != nil {
		serverError(w, err)
		return
	}
	delete(s.runnerSeen, runnerID)
	delete(s.runnerReady, runnerID)
	writeRunnerToken(w, http.StatusOK, runnerID, token)
}

func writeRunnerToken(w http.ResponseWriter, status int, name, token string) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]string{"name": name, "token": token})
}
