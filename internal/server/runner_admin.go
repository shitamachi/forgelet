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
	Digest string `json:"digest,omitempty"`
	Paused bool   `json:"paused,omitempty"`
	// Info, Labels and LastSeen are the device's last report, kept so an
	// offline device still shows what and when it was.
	Info     *run.RunnerInfo `json:"info,omitempty"`
	Labels   []string        `json:"labels,omitempty"`
	LastSeen time.Time       `json:"last_seen,omitzero"`
}

type runnerView struct {
	Name            string          `json:"name"`
	Status          string          `json:"status"`
	Paused          bool            `json:"paused"`
	LastSeen        time.Time       `json:"last_seen,omitzero"`
	JobID           string          `json:"job_id,omitempty"`
	RunID           string          `json:"run_id,omitempty"`
	JobName         string          `json:"job_name,omitempty"`
	Repository      string          `json:"repository,omitempty"`
	StartedAt       time.Time       `json:"started_at,omitzero"`
	DurationSeconds int             `json:"duration_seconds,omitempty"`
	Labels          []string        `json:"labels,omitempty"`
	Info            *run.RunnerInfo `json:"info,omitempty"`
}

// runnerRegistered reports whether a device with this credential identity is
// registered. A device is one machine; there is no separate policy document.
func (s *Server) runnerRegistered(runnerID string) bool {
	s.runnerMu.Lock()
	defer s.runnerMu.Unlock()
	_, ok := s.runners[runnerID]
	return ok
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
		if !runnerName.MatchString(name) || entry.Digest == "" {
			return fmt.Errorf("invalid runner registration %q", name)
		}
		if _, err := hex.DecodeString(entry.Digest); err != nil {
			return fmt.Errorf("invalid runner registration %q", name)
		}
	}
	return nil
}

func (s *Server) saveRunnerLocked(ctx context.Context, runnerID string, entry runnerRegistration) error {
	next := make(map[string]runnerRegistration, len(s.runners)+1)
	maps.Copy(next, s.runners)
	next[runnerID] = entry
	return s.saveRunnersLocked(ctx, next)
}

// saveRunnersLocked persists the whole registration set. An empty set is stored
// as an empty object so loading never sees a NULL value.
func (s *Server) saveRunnersLocked(ctx context.Context, next map[string]runnerRegistration) error {
	body := []byte("{}")
	if len(next) > 0 {
		encoded, err := json.Marshal(next)
		if err != nil {
			return fmt.Errorf("encode runner registrations: %w", err)
		}
		body = encoded
	}
	if err := s.external.SaveRunnerRegistrations(ctx, body); err != nil {
		return fmt.Errorf("save runner registration: %w", err)
	}
	s.runners = next
	return nil
}

// deleteRunner removes a paired device. A device with a live lease is left
// until the job ends.
func (s *Server) deleteRunner(w http.ResponseWriter, r *http.Request) {
	runnerID := chi.URLParam(r, "runnerID")
	s.runnerMu.Lock()
	defer s.runnerMu.Unlock()
	if _, exists := s.runners[runnerID]; !exists {
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
	next := make(map[string]runnerRegistration, len(s.runners))
	maps.Copy(next, s.runners)
	delete(next, runnerID)
	if err = s.saveRunnersLocked(r.Context(), next); err != nil {
		serverError(w, err)
		return
	}
	delete(s.runnerSeen, runnerID)
	delete(s.runnerReady, runnerID)
	delete(s.runnerInfo, runnerID)
	delete(s.runnerLabels, runnerID)
	delete(s.runnerSaved, runnerID)
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

// runnerInfoView prefers the live report and falls back to the last persisted
// one, so an offline device still explains itself.
func (s *Server) runnerInfoView(runnerID string) *run.RunnerInfo {
	if info, ok := s.runnerInfo[runnerID]; ok {
		copied := info
		return &copied
	}
	if info := s.runners[runnerID].Info; info != nil {
		copied := *info
		return &copied
	}
	return nil
}

// infoPersistInterval bounds how often a device's numbers reach SQLite. The
// live copy is always current; the persisted one only has to survive a restart,
// and the last-seen time it keeps is at most this stale.
const infoPersistInterval = time.Minute

// recordRunnerReport keeps one poll's capabilities, readings and time. Polls are
// frequent, so persistence is throttled and its failure never fails the poll.
func (s *Server) recordRunnerReport(ctx context.Context, runnerID string, info *run.RunnerInfo, labels []string) {
	now := s.now().UTC()
	if info != nil {
		if !info.ValidBounds() {
			info = nil
		} else {
			copied := *info
			copied.ReportedAt = now
			s.runnerInfo[runnerID] = copied
		}
	}
	if len(labels) > 0 {
		s.runnerLabels[runnerID] = labels
	}
	entry := s.runners[runnerID]
	entry.LastSeen = now
	if last, ok := s.runnerSaved[runnerID]; ok && now.Sub(last) < infoPersistInterval {
		return
	}
	if info != nil {
		copied := s.runnerInfo[runnerID]
		entry.Info = &copied
	}
	if len(labels) > 0 {
		entry.Labels = labels
	}
	if err := s.saveRunnerLocked(ctx, runnerID, entry); err != nil {
		return
	}
	s.runnerSaved[runnerID] = now
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
	views := make([]runnerView, 0, len(s.runners))
	for _, name := range slices.Sorted(maps.Keys(s.runners)) {
		// Live polls decide whether the device is online; the persisted time
		// answers "when was it last here" after a restart, with no poll in memory.
		seen := s.runnerSeen[name]
		lastSeen := seen
		if lastSeen.IsZero() {
			lastSeen = s.runners[name].LastSeen
		}
		labels := s.runnerLabels[name]
		if len(labels) == 0 {
			labels = s.runners[name].Labels
		}
		view := runnerView{Name: name,
			LastSeen: lastSeen, Paused: s.runners[name].Paused, Status: "offline",
			Labels: labels, Info: s.runnerInfoView(name)}
		if job := jobs[name]; job.ID != "" {
			view.Status, view.JobID, view.RunID = "busy", job.ID, job.RunID
			view.JobName, view.Repository = job.Name, job.Plan.Repository
			view.StartedAt = job.ClaimedAt
			if view.StartedAt.IsZero() {
				view.StartedAt = job.ReservedAt
			}
			if !view.StartedAt.IsZero() && now.After(view.StartedAt) {
				view.DurationSeconds = int(now.Sub(view.StartedAt) / time.Second)
			}
		} else if view.Paused {
			view.Status = "paused"
		} else if !seen.IsZero() && now.Sub(seen) < 30*time.Second {
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
	if _, exists := s.runners[runnerID]; !exists {
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
	if _, exists := s.runners[runnerID]; !exists {
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
