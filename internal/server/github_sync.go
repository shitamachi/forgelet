package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	gh "github.com/shitamachi/forgelet/internal/provider/github"
)

var errAuthorizationSuperseded = errors.New("GitHub authorization changed during discovery; refresh again")

func (s *Server) loadGitHubAuthorization(ctx context.Context) error {
	raw, err := s.store.GitHubAuthorization(ctx)
	if err != nil {
		return fmt.Errorf("read GitHub authorization: %w", err)
	}
	if len(raw) == 0 {
		return nil
	}
	var state GitHubAuthorization
	if err := json.Unmarshal(raw, &state); err != nil {
		return fmt.Errorf("decode GitHub authorization: %w", err)
	}
	if state.Version < 0 || len(state.Deliveries) > 128 || len(state.Quarantined) > 1000 {
		return fmt.Errorf("invalid persisted GitHub authorization")
	}
	s.authorization.Store(&state)
	if _, ok := s.provider.(onboardingProvider); ok && state.Catalog != nil {
		// A restart may have missed a revocation webhook while offline. Retain the
		// successful catalog for display, but confirm grants before issuing new work.
		_, err = s.mutateAuthorization(ctx, func(next *GitHubAuthorization) error {
			next.Pending = true
			for _, inst := range next.Catalog.Installations {
				if !slices.Contains(next.Quarantined, inst.ID) {
					next.Quarantined = append(next.Quarantined, inst.ID)
				}
			}
			return nil
		})
	}
	return err
}

func (s *Server) mutateAuthorization(ctx context.Context, mutate func(*GitHubAuthorization) error) (*GitHubAuthorization, error) {
	s.authorizationMu.Lock()
	defer s.authorizationMu.Unlock()
	raw, err := s.store.GitHubAuthorization(ctx)
	if err != nil {
		return nil, err
	}
	var next GitHubAuthorization
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &next); err != nil {
			return nil, fmt.Errorf("decode authorization snapshot: %w", err)
		}
	}
	if err := mutate(&next); err != nil {
		return nil, err
	}
	next.Version++
	body, err := json.Marshal(next)
	if err != nil {
		return nil, err
	}
	if err := s.store.SaveGitHubAuthorization(ctx, raw, body); err != nil {
		return nil, err
	}
	// Materialize from the persisted bytes, never provider-owned mutable slices.
	var immutable GitHubAuthorization
	if err := json.Unmarshal(body, &immutable); err != nil {
		return nil, err
	}
	s.authorization.Store(&immutable)
	return &immutable, nil
}

func (s *Server) refreshGitHubAuthorization(ctx context.Context) (*GitHubAuthorization, error) {
	provider, ok := s.provider.(onboardingProvider)
	if !ok {
		return nil, errors.New("GitHub App discovery is not configured")
	}
	select {
	case s.authorizationRefresh <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-s.authorizationRefresh }()
	started, err := s.mutateAuthorization(ctx, func(next *GitHubAuthorization) error {
		next.AttemptedAt = s.now().UTC()
		next.Pending = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	catalog, fetchErr := provider.Discover(ctx)
	if fetchErr != nil {
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_, saveErr := s.mutateAuthorization(saveCtx, func(next *GitHubAuthorization) error {
			next.Error = "GitHub 授权同步失败；保留最近成功结果，请稍后刷新。"
			next.Pending = true
			return nil
		})
		return nil, errors.Join(fmt.Errorf("GitHub authorization discovery failed: %w", fetchErr), saveErr)
	}
	next, err := s.mutateAuthorization(ctx, func(next *GitHubAuthorization) error {
		if next.Version != started.Version {
			return errAuthorizationSuperseded
		}
		identities := map[string]int64{}
		for _, repo := range s.management().Repositories {
			if id := installationForRepository(repo, next); id > 0 {
				identities[strings.ToLower(repo.Name)] = id
			}
		}
		next.RepositoryInstallations = identities
		next.Catalog = &catalog
		next.CheckedAt = s.now().UTC()
		next.Error = ""
		next.Pending = false
		next.Quarantined = nil
		return nil
	})
	if errors.Is(err, errAuthorizationSuperseded) {
		s.wakeGitHubAuthorization()
	}
	return next, err
}

func (s *Server) wakeGitHubAuthorization() {
	if s.authorizationWake == nil {
		return
	}
	select {
	case s.authorizationWake <- struct{}{}:
	default:
	}
}

// RunGitHubAuthorization has one cancellable timer/reader for the process. A
// webhook only persists a hint and wakes this loop; it never starts a goroutine.
func (s *Server) RunGitHubAuthorization(ctx context.Context) error {
	if _, ok := s.provider.(onboardingProvider); !ok {
		return nil
	}
	for {
		refreshCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		_, err := s.refreshGitHubAuthorization(refreshCtx)
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		delay := 5 * time.Minute
		if err != nil {
			delay = 15 * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-s.authorizationWake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

type authorizationSnapshotView struct {
	CheckedAt   time.Time   `json:"checked_at,omitzero"`
	AttemptedAt time.Time   `json:"attempted_at,omitzero"`
	Pending     bool        `json:"pending"`
	Error       string      `json:"error,omitempty"`
	Catalog     *gh.Catalog `json:"catalog,omitempty"`
}
type authorizationView struct {
	Snapshot     *authorizationSnapshotView         `json:"snapshot,omitempty"`
	Repositories map[string]RepositoryAuthorization `json:"repositories"`
}

func (s *Server) githubAuthorizationStatus(w http.ResponseWriter, r *http.Request) {
	snapshot := s.authorization.Load()
	out := authorizationView{Repositories: map[string]RepositoryAuthorization{}}
	if snapshot != nil {
		out.Snapshot = &authorizationSnapshotView{CheckedAt: snapshot.CheckedAt, AttemptedAt: snapshot.AttemptedAt, Pending: snapshot.Pending, Error: snapshot.Error, Catalog: snapshot.Catalog}
	}
	for _, repo := range s.management().Repositories {
		out.Repositories[repo.Name] = s.repositoryAccess(repo.Name, 0)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) refreshGitHubAuthorizationHTTP(w http.ResponseWriter, r *http.Request) {
	if _, err := s.refreshGitHubAuthorization(r.Context()); err != nil {
		http.Error(w, "GitHub 授权同步暂不可用；上次成功结果已保留。", http.StatusBadGateway)
		return
	}
	s.githubAuthorizationStatus(w, r)
}
