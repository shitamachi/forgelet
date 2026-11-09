package server

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/shitamachi/forgelet/internal/kube"
	"github.com/shitamachi/forgelet/internal/run"
	"github.com/shitamachi/forgelet/internal/storage"
)

const runnerReadyWindow = 10 * time.Second

// peerMargin is how much better another idle device must look before this one
// gives up a round. The hysteresis keeps two similar machines from alternating.
const peerMargin = 0.15

// stepAsideForPeer reports whether an idle device with clearly more headroom
// can take the queued work this one would otherwise claim. The better device
// polls on its own schedule, so no job is reserved away from it; the worse
// device simply retries after its normal wait and takes the job when the peer
// is busy, paused, offline or done. Scheduling stays a pull: no extra request
// is sent to any device, and a busy machine never sees scheduling traffic.
//
// The peer must cover every label and build platform this device has, so a job
// that only this device could run is never held back for a peer that cannot
// take it. If the peer disappears, its readiness expires within the window.
func (s *Server) stepAsideForPeer(ctx context.Context, runnerID string, labels, platforms []string, now time.Time) bool {
	self, known := s.runnerInfo[runnerID]
	if !known || len(labels) == 0 {
		return false
	}
	queued, err := s.external.HasQueuedExternal(ctx, labels)
	if err != nil || !queued {
		return false
	}
	selfScore := self.Score()
	for id, ready := range s.runnerReady {
		if id == runnerID || now.Sub(ready.at) >= runnerReadyWindow || s.runners[id].Paused {
			continue
		}
		if !covers(ready.labels, labels) || !covers(ready.platforms, platforms) {
			continue
		}
		peer, ok := s.runnerInfo[id]
		if !ok || peer.Score()+peerMargin >= selfScore {
			continue
		}
		return true
	}
	return false
}

// covers reports whether a peer advertises everything this device does. An
// empty self list is covered by anything.
func covers(peer, self []string) bool {
	for _, value := range self {
		if !slices.Contains(peer, value) {
			return false
		}
	}
	return true
}

type runnerAvailability struct {
	at        time.Time
	platforms []string
	labels    []string
}

func (s *Server) preferRunner() bool {
	return s.cfg.PreferRunner == nil || *s.cfg.PreferRunner
}

func runnerPlatforms(header string) []string {
	if header == "" {
		return nil // Legacy client.
	}
	platforms := []string{}
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		if slices.Contains(strings.Split(header, ","), platform) {
			platforms = append(platforms, platform)
		}
	}
	return platforms
}

func (s *Server) reservePods(ctx context.Context, limit int, repos []string) ([]run.Job, error) {
	if !s.preferRunner() || limit == 0 {
		return s.store.ReserveAuthorized(ctx, limit, repos)
	}
	// Poll and Pod selection share the lock. An idle announcement can't race
	// a fallback reservation, and the active lease check includes restarts.
	s.runnerMu.Lock()
	defer s.runnerMu.Unlock()
	var idle map[string]storage.RunnerCapacity
	now := s.now()
	for id, ready := range s.runnerReady {
		if now.Sub(ready.at) >= runnerReadyWindow {
			delete(s.runnerReady, id)
			continue
		}
		if !s.runners[id].Paused {
			if idle == nil {
				idle = map[string]storage.RunnerCapacity{}
			}
			idle[id] = storage.RunnerCapacity{Platforms: ready.platforms, Labels: ready.labels}
		}
	}
	var capacity []storage.RunnerCapacity
	if len(idle) > 0 {
		active, err := s.store.Active(ctx)
		if err != nil {
			return nil, err
		}
		for _, job := range active {
			delete(idle, job.ExternalRunnerID())
		}
		capacity = slices.Collect(maps.Values(idle))
		slices.SortFunc(capacity, func(a, b storage.RunnerCapacity) int { return len(a.Platforms) - len(b.Platforms) })
	}
	return s.store.ReserveAuthorized(ctx, limit, repos, capacity...)
}

func (s *Server) fallbackPodProfile(plan run.Plan) (kube.Profile, error) {
	name := plan.FallbackProfile
	profile, ok := s.cfg.Profiles[name]
	if !ok || profile.Act != nil {
		return kube.Profile{}, fmt.Errorf("runner fallback %q requires a native Pod profile", name)
	}
	build := plan.ExternalBuild
	if build == nil {
		return profile, nil
	}
	if !build.Valid() || profile.Build == nil || !slices.Contains(profile.Build.Images, build.Image) ||
		profile.Build.InsecureRegistry != build.InsecureRegistry {
		return kube.Profile{}, fmt.Errorf("runner fallback %q does not authorize the configured image repository and registry policy", name)
	}
	os, arch, _ := strings.Cut(build.Platform, "/")
	selector := maps.Clone(profile.NodeSelector)
	if selector == nil {
		selector = map[string]string{}
	}
	for key, value := range map[string]string{corev1.LabelOSStable: os, corev1.LabelArchStable: arch} {
		if current, exists := selector[key]; exists && current != value {
			return kube.Profile{}, fmt.Errorf("runner fallback %q node selector conflicts with image platform %s", name, build.Platform)
		}
		selector[key] = value
	}
	profile.NodeSelector = selector
	return profile, nil
}
