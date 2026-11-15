package server

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

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
func (s *Server) stepAsideForPeer(ctx context.Context, runnerID string, capacity storage.RunnerCapacity, now time.Time) bool {
	self, known := s.runnerInfo[runnerID]
	if !known || len(capacity.Labels) == 0 {
		return false
	}
	queued, err := s.external.HasQueuedExternal(ctx, capacity.Labels)
	if err != nil || !queued {
		return false
	}
	selfScore := self.Score()
	for id, ready := range s.runnerReady {
		if id == runnerID || !now.Before(ready.expiresAt) || s.runners[id].Paused {
			continue
		}
		if !covers(ready.capacity.Labels, capacity.Labels) || !covers(ready.capacity.Platforms, capacity.Platforms) ||
			!covers(ready.capacity.Containers, capacity.Containers) {
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
func covers[T comparable](peer, self []T) bool {
	for _, value := range self {
		if !slices.Contains(peer, value) {
			return false
		}
	}
	return true
}

type runnerAvailability struct {
	expiresAt time.Time
	capacity  storage.RunnerCapacity
}

func approvedRunnerContainers(cfg Config) []run.RunnerContainer {
	var containers []run.RunnerContainer
	for _, name := range cfg.RunnerFallbacks {
		profile := cfg.Profiles[name]
		container := run.RunnerContainer{Image: profile.Image, Platform: profile.RunnerPlatform}
		if profile.Act != nil && cfg.RunnerActImage != "" {
			var err error
			container, err = actRunnerContainer(cfg, profile)
			if err != nil {
				continue
			}
		}
		if container.Valid() && !slices.Contains(containers, container) {
			containers = append(containers, container)
		}
	}
	slices.SortFunc(containers, func(a, b run.RunnerContainer) int {
		if n := strings.Compare(a.Image, b.Image); n != 0 {
			return n
		}
		return strings.Compare(a.Platform, b.Platform)
	})
	return containers
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
		if !now.Before(ready.expiresAt) {
			delete(s.runnerReady, id)
			continue
		}
		if !s.runners[id].Paused {
			if idle == nil {
				idle = map[string]storage.RunnerCapacity{}
			}
			idle[id] = ready.capacity
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
	if !ok {
		return kube.Profile{}, fmt.Errorf("runner fallback %q requires a native Pod profile", name)
	}
	if c := plan.RunnerContainer; c != nil && c.Kernel == "act" {
		if !c.Valid() || profile.Act == nil {
			return kube.Profile{}, fmt.Errorf("invalid act fallback environment")
		}
		a := *profile.Act
		a.RunnerImage = c.JobImage
		a.Platform = c.Platform
		a.BuildkitConfig = c.BuildkitConfig
		a.CPULimit = strconv.FormatInt(c.CPUs, 10)
		a.CacheObjectBytes = c.CacheObjectBytes
		a.MemoryLimit = strconv.FormatInt(c.MemoryBytes, 10)
		a.DataLimit = strconv.FormatInt(c.DiskBytes, 10)
		profile.Act = &a
		selector, err := platformNodeSelector(profile.NodeSelector, c.Platform)
		if err != nil {
			return kube.Profile{}, err
		}
		profile.NodeSelector = selector
		return profile, nil
	}
	if profile.Act != nil {
		return kube.Profile{}, fmt.Errorf("runner fallback %q requires a native Pod profile", name)
	}
	if container := plan.RunnerContainer; container != nil {
		if !container.Valid() {
			return kube.Profile{}, fmt.Errorf("runner fallback %q has an invalid container environment", name)
		}
		profile.Image = container.Image
		profile.RunnerPlatform = container.Platform
		selector, err := platformNodeSelector(profile.NodeSelector, container.Platform)
		if err != nil {
			return kube.Profile{}, fmt.Errorf("runner fallback %q: %w", name, err)
		}
		profile.NodeSelector = selector
	}
	build := plan.ExternalBuild
	if build == nil {
		return profile, nil
	}
	if !build.Valid() || profile.Build == nil || !slices.Contains(profile.Build.Images, build.Image) ||
		profile.Build.InsecureRegistry != build.InsecureRegistry {
		return kube.Profile{}, fmt.Errorf("runner fallback %q does not authorize the configured image repository and registry policy", name)
	}
	selector, err := platformNodeSelector(profile.NodeSelector, build.Platform)
	if err != nil {
		return kube.Profile{}, fmt.Errorf("runner fallback %q: %w", name, err)
	}
	profile.NodeSelector = selector
	return profile, nil
}

func actRunnerContainer(cfg Config, p kube.Profile) (run.RunnerContainer, error) {
	a := p.Act
	if a == nil {
		return run.RunnerContainer{}, fmt.Errorf("act profile required")
	}
	platform := a.Platform
	if platform == "" {
		platform = "linux/amd64"
	}
	image := a.RunnerImage
	if image == "" {
		image = p.Image
	}
	cpus := int64(3)
	if a.CPULimit != "" {
		q, err := resource.ParseQuantity(a.CPULimit)
		if err != nil {
			return run.RunnerContainer{}, err
		}
		cpus = q.Value()
	}
	memory := int64(4 << 30)
	if a.MemoryLimit != "" {
		q, err := resource.ParseQuantity(a.MemoryLimit)
		if err != nil {
			return run.RunnerContainer{}, err
		}
		memory = q.Value()
	}
	disk := int64(24 << 30)
	if a.DataLimit != "" {
		q, err := resource.ParseQuantity(a.DataLimit)
		if err != nil {
			return run.RunnerContainer{}, err
		}
		disk = q.Value()
	}
	c := run.RunnerContainer{Image: cfg.RunnerActImage, Platform: platform, Kernel: "act", JobImage: image, BuildkitConfig: a.BuildkitConfig, CPUs: cpus, CacheObjectBytes: a.CacheObjectBytes, MemoryBytes: memory, DiskBytes: disk}
	if !c.Valid() {
		return c, fmt.Errorf("invalid act runner environment")
	}
	return c, nil
}

func platformNodeSelector(base map[string]string, platform string) (map[string]string, error) {
	os, arch, _ := strings.Cut(platform, "/")
	selector := maps.Clone(base)
	if selector == nil {
		selector = map[string]string{}
	}
	for key, value := range map[string]string{corev1.LabelOSStable: os, corev1.LabelArchStable: arch} {
		if current, exists := selector[key]; exists && current != value {
			return nil, fmt.Errorf("node selector conflicts with image platform %s", platform)
		}
		selector[key] = value
	}
	return selector, nil
}
