package server

import (
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/shitamachi/forgelet/internal/delivery"
)

// consoleConfig exposes only the operational configuration needed by the UI.
// Credentials, encryption keys and registry Secret references stay server-side.
func (s *Server) consoleConfig(w http.ResponseWriter, _ *http.Request) {
	type profile struct {
		Name  string `json:"name"`
		Image string `json:"image"`
		Build bool   `json:"build"`
	}
	profiles := make([]profile, 0, len(s.cfg.Profiles))
	for name, p := range s.cfg.Profiles {
		profiles = append(profiles, profile{Name: name, Image: p.Image, Build: p.Build != nil})
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	managed := s.management()
	repositories := []string{}
	for _, r := range managed.Repositories {
		if r.Enabled {
			repositories = append(repositories, r.Name)
		}
	}
	sort.Strings(repositories)
	targets := make([]delivery.Target, 0, len(managed.Targets))
	targets = append(targets, managed.Targets...)
	sort.Slice(targets, func(i, j int) bool { return targets[i].Name < targets[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{
		"repositories": repositories, "profiles": profiles, "targets": targets,
		"max_parallel": s.cfg.MaxParallel,
		"features":     map[string]bool{"github": s.provider != nil, "secrets": len(s.cfg.SecretKey) == 32, "artifacts": s.cfg.Blobs != nil, "delivery": s.cfg.Delivery != nil},
		"retention":    map[string]int64{"history": int64(s.cfg.Retention.History.Seconds()), "logs": int64(s.cfg.Retention.Logs.Seconds()), "events": int64(s.cfg.Retention.Events.Seconds()), "backups": int64(s.cfg.Retention.Backups.Seconds())},
	})
}

// Open management is an explicit trusted-network mode, not a user/session system.
// Browser mutations must be same-origin JSON; runtime identity and webhook HMAC
// are verified independently of management access.
func (s *Server) openManagement(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		http.Error(w, "cross-site management request rejected", http.StatusForbidden)
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		scheme := "http"
		public, _ := url.Parse(s.cfg.PublicURL)
		if r.TLS != nil || (public != nil && public.Scheme == "https" && strings.EqualFold(public.Host, r.Host)) {
			scheme = "https"
		}
		if err != nil || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Scheme != scheme || !strings.EqualFold(u.Host, r.Host) {
			http.Error(w, "cross-origin management request rejected", http.StatusForbidden)
			return false
		}
	}
	if !strings.EqualFold(strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0]), "application/json") {
		http.Error(w, "management mutations require application/json", http.StatusUnsupportedMediaType)
		return false
	}
	return true
}
