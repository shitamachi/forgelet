package server

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/shitamachi/forgelet/internal/delivery"
	"github.com/shitamachi/forgelet/internal/run"
)

func (s *Server) requestDelivery(w http.ResponseWriter, r *http.Request) {
	job, _, err := s.authenticate(r)
	if err != nil {
		http.Error(w, "invalid workload identity", http.StatusUnauthorized)
		return
	}
	if job.State != run.Running || job.CancelRequested {
		http.Error(w, "job is not running", http.StatusConflict)
		return
	}
	if s.cfg.Delivery == nil {
		http.Error(w, "GitOps delivery unavailable", http.StatusServiceUnavailable)
		return
	}
	var input struct {
		Target string `json:"target"`
		Digest string `json:"digest"`
	}
	if err := decode(w, r, &input, 4096); err != nil || !run.ValidDigest(input.Digest) {
		http.Error(w, "target and sha256 digest required", http.StatusBadRequest)
		return
	}
	target, ok := s.target(input.Target)
	if !ok || target.SourceRepository != job.Plan.Repository || target.SourceRef != job.Plan.Ref || (job.Plan.EventName != "push" && job.Plan.EventName != "workflow_dispatch" && job.Plan.EventName != "") {
		http.Error(w, "deployment target is not permitted for this repository/ref/event", http.StatusForbidden)
		return
	}
	now := s.now().UTC()
	item := delivery.Record{ID: delivery.ID(job.RunID, target.Name), Target: target, RunID: job.RunID, JobID: job.ID, Digest: input.Digest, State: delivery.Queued, CreatedAt: now, UpdatedAt: now}
	saved, err := s.cfg.Delivery.Store.EnqueueDelivery(r.Context(), item)
	respond(w, saved, err)
}
func (s *Server) listDeliveries(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Delivery == nil {
		writeJSON(w, 200, []delivery.Record{})
		return
	}
	items, err := s.cfg.Delivery.Store.Deliveries(r.Context(), false)
	for i := range items {
		items[i].Content = nil
	}
	respond(w, items, err)
}
func (s *Server) getDelivery(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Delivery == nil {
		http.Error(w, "GitOps unavailable", http.StatusServiceUnavailable)
		return
	}
	item, err := s.cfg.Delivery.Store.GetDelivery(r.Context(), chi.URLParam(r, "deploymentID"))
	item.Content = nil
	respond(w, item, err)
}
func (s *Server) rollback(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Delivery == nil {
		http.Error(w, "GitOps unavailable", http.StatusServiceUnavailable)
		return
	}
	s.tickMu.Lock()
	defer s.tickMu.Unlock()
	item, err := s.cfg.Delivery.Rollback(r.Context(), chi.URLParam(r, "deploymentID"))
	item.Content = nil
	respond(w, item, err)
}
func validateDeliveryConfig(cfg Config) error {
	if len(cfg.Targets) > 32 {
		return fmt.Errorf("at most 32 delivery targets")
	}
	if len(cfg.Targets) > 0 && cfg.Delivery == nil {
		return fmt.Errorf("delivery targets require GitHub and Flux adapters")
	}
	for name, t := range cfg.Targets {
		if name != t.Name {
			return fmt.Errorf("target key/name mismatch")
		}
		if err := t.Validate(); err != nil {
			return fmt.Errorf("target %s: %w", name, err)
		}
	}
	return nil
}
