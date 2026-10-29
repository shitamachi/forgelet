package server

import (
	"context"
	"errors"
	"slices"
	"testing"

	gh "github.com/shitamachi/forgelet/internal/provider/github"
)

func TestAuthorizationRefreshFailureAndProviderMutationPreserveSnapshot(t *testing.T) {
	h, p := onboardingHarness(t)
	first, err := h.server.refreshGitHubAuthorization(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	p.catalog.Repositories[0].Name = "changed/provider-owned-slice"
	if got := h.server.authorization.Load().Catalog.Repositories[0].Name; got != "me/repo" {
		t.Fatal("provider mutated published snapshot", got)
	}
	p.err = errors.New("remote request failed")
	if _, err := h.server.refreshGitHubAuthorization(t.Context()); err == nil {
		t.Fatal("discovery failure ignored")
	}
	next := h.server.authorization.Load()
	if next.Catalog == nil || next.Catalog.Repositories[0].Name != "me/repo" || !next.CheckedAt.Equal(first.CheckedAt) || next.Error == "" || !next.Pending {
		t.Fatalf("failed refresh erased authority: %+v", next)
	}
	raw, err := h.store.GitHubAuthorization(t.Context())
	if err != nil || len(raw) == 0 {
		t.Fatal("snapshot not durable", err)
	}
}

type blockedAuthorizationProvider struct {
	*onboardingFake
	entered chan struct{}
	release chan struct{}
}

func (p *blockedAuthorizationProvider) Discover(ctx context.Context) (gh.Catalog, error) {
	close(p.entered)
	select {
	case <-p.release:
		return p.catalog, nil
	case <-ctx.Done():
		return gh.Catalog{}, ctx.Err()
	}
}
func TestAuthorizationEventSupersedesInFlightFullSnapshot(t *testing.T) {
	h, p := onboardingHarness(t)
	if _, err := h.server.refreshGitHubAuthorization(t.Context()); err != nil {
		t.Fatal(err)
	}
	blocked := &blockedAuthorizationProvider{onboardingFake: p, entered: make(chan struct{}), release: make(chan struct{})}
	h.server.provider = blocked
	done := make(chan error, 1)
	go func() { _, err := h.server.refreshGitHubAuthorization(t.Context()); done <- err }()
	<-blocked.entered
	if _, err := h.server.mutateAuthorization(t.Context(), func(next *GitHubAuthorization) error { next.Pending = true; next.Quarantined = []int64{42}; return nil }); err != nil {
		t.Fatal(err)
	}
	close(blocked.release)
	if err := <-done; !errors.Is(err, errAuthorizationSuperseded) {
		t.Fatal("stale discovery cleared newer event", err)
	}
	current := h.server.authorization.Load()
	if !current.Pending || !slices.Contains(current.Quarantined, int64(42)) {
		t.Fatal(current)
	}
	h.server.provider = p
	if _, err := h.server.refreshGitHubAuthorization(t.Context()); err != nil {
		t.Fatal(err)
	}
	if next := h.server.authorization.Load(); next.Pending || len(next.Quarantined) > 0 {
		t.Fatal("fresh authority did not clear quarantine", next)
	}
}

func TestAuthorizationRestartRequiresFreshConfirmation(t *testing.T) {
	h, _ := onboardingHarness(t)
	if _, err := h.server.refreshGitHubAuthorization(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := h.server.loadGitHubAuthorization(t.Context()); err != nil {
		t.Fatal(err)
	}
	next := h.server.authorization.Load()
	if next.Catalog == nil || !next.Pending || !slices.Contains(next.Quarantined, int64(42)) {
		t.Fatalf("offline grants trusted without recheck: %+v", next)
	}
}

func TestAuthorizationBackgroundLoopStopsBlockedIOOnCancellation(t *testing.T) {
	h, p := onboardingHarness(t)
	blocked := &blockedAuthorizationProvider{onboardingFake: p, entered: make(chan struct{}), release: make(chan struct{})}
	h.server.provider = blocked
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.server.RunGitHubAuthorization(ctx) }()
	<-blocked.entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("background discovery survived cancellation", err)
	}
}

func TestLegacyInstallationIdentitySurvivesSuspendedRepositoryOmission(t *testing.T) {
	h, p := onboardingHarness(t)
	// Two complete reads exercise persistence of a name-only configured binding.
	if _, err := h.server.refreshGitHubAuthorization(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.server.refreshGitHubAuthorization(t.Context()); err != nil {
		t.Fatal(err)
	}
	now := h.server.now()
	p.catalog.Installations[0].SuspendedAt = &now
	p.catalog.Repositories = nil // GitHub cannot enumerate repositories of suspended installs.
	if _, err := h.server.refreshGitHubAuthorization(t.Context()); err != nil {
		t.Fatal(err)
	}
	if state := h.server.repositoryAccess("me/repo", 0).State; state != "suspended" {
		t.Fatal(state)
	}
	p.catalog.Installations = nil
	if _, err := h.server.refreshGitHubAuthorization(t.Context()); err != nil {
		t.Fatal(err)
	}
	if state := h.server.repositoryAccess("me/repo", 0).State; state != "not_authorized" {
		t.Fatal("identity hint granted missing installation", state)
	}
}
