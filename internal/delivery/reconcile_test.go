package delivery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run"
)

func testTarget() Target {
	return Target{Name: "app", SourceRepository: "test/source", SourceRef: "refs/heads/main", Repository: "test/gitops", InstallationID: 1, Branch: "main", Path: "apps/kustomization.yaml", Mode: "direct", Image: "ghcr.io/test/app", ImageName: "app", FluxNamespace: "flux-system", Kustomization: "app", Namespace: "apps", Deployment: "app", Container: "app"}
}
func manifest(digest string) []byte {
	return []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: [deployment.yaml]\nimages:\n  - name: app\n    newName: ghcr.io/test/app\n    digest: " + digest + "\n")
}

type memoryStore struct {
	records []Record
	state   run.State
}

func (s *memoryStore) EnqueueDelivery(_ context.Context, r Record) (Record, error) {
	for _, old := range s.records {
		if old.ID == r.ID {
			return old, nil
		}
	}
	s.records = append(s.records, r)
	return r, nil
}
func (s *memoryStore) SaveDelivery(_ context.Context, r Record) error {
	for i := range s.records {
		if s.records[i].ID == r.ID {
			s.records[i] = r
			return nil
		}
	}
	return errors.New("missing")
}
func (s *memoryStore) GetDelivery(_ context.Context, id string) (Record, error) {
	for _, r := range s.records {
		if r.ID == id {
			return r, nil
		}
	}
	return Record{}, ErrNotFound
}
func (s *memoryStore) Deliveries(_ context.Context, active bool) ([]Record, error) {
	var records []Record
	for _, r := range s.records {
		if !active || r.Active() {
			records = append(records, r)
		}
	}
	return records, nil
}
func (s *memoryStore) GetRun(context.Context, string) (run.Run, error) {
	return run.Run{State: s.state}, nil
}

type repository struct {
	reads      int
	snap       Snapshot
	published  int
	publishErr error
	pr         PullRequest
	prErr      error
	contains   bool
}

func (r *repository) Snapshot(context.Context, Target, string) (Snapshot, error) {
	r.reads++
	return r.snap, nil
}
func (r *repository) Prepare(context.Context, Record) (string, error) {
	return strings.Repeat("b", 40), nil
}
func (r *repository) Publish(_ context.Context, record Record) (Publication, error) {
	if r.publishErr != nil {
		return Publication{}, r.publishErr
	}
	r.published++
	if record.Target.Mode == "pr" {
		return Publication{PR: 1}, nil
	}
	r.snap.SHA = record.Commit
	r.snap.Content = record.Content
	return Publication{Revision: record.Commit}, nil
}
func (r *repository) PullRequest(context.Context, Record) (PullRequest, error) { return r.pr, r.prErr }
func (r *repository) Contains(context.Context, Target, string, string) (bool, error) {
	return r.contains, nil
}

type observer struct{ observation Observation }

func (o *observer) Observe(context.Context, Target) (Observation, error) { return o.observation, nil }
func setup(t *testing.T) (*Coordinator, *memoryStore, *repository, *observer) {
	t.Helper()
	now := time.Now()
	s := &memoryStore{records: []Record{{ID: "one", Target: testTarget(), RunID: "run", Digest: "sha256:" + strings.Repeat("b", 64), State: Queued, CreatedAt: now}}, state: run.Running}
	r := &repository{snap: Snapshot{SHA: strings.Repeat("a", 40), Content: manifest("sha256:" + strings.Repeat("a", 64))}, contains: true}
	o := &observer{}
	return &Coordinator{Store: s, Repository: r, Observer: o, Now: func() time.Time { return now }}, s, r, o
}
func tick(t *testing.T, c *Coordinator, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := c.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDeliveryWaitsForRunAndObservedRevisionHealth(t *testing.T) {
	c, s, r, o := setup(t)
	tick(t, c, 2)
	if r.published != 0 || s.records[0].State != Queued {
		t.Fatal("published running source")
	}
	s.state = run.Succeeded
	tick(t, c, 3)
	if r.published != 1 || s.records[0].State != Delivered {
		t.Fatal(s.records)
	}
	o.observation = Observation{Revision: "main@sha1:" + strings.Repeat("a", 40), Image: testTarget().Image + "@" + s.records[0].Digest, Ready: true}
	tick(t, c, 1)
	if s.records[0].State != Delivered {
		t.Fatal("accepted old revision")
	}
	o.observation.Revision = "main@sha1:" + s.records[0].Revision
	o.observation.Ready = false
	tick(t, c, 1)
	if s.records[0].State != Delivered {
		t.Fatal("accepted unhealthy")
	}
	o.observation.Ready = true
	tick(t, c, 1)
	if s.records[0].State != Deployed {
		t.Fatal(s.records)
	}
	// A later reconciliation can report degradation; success is never latched.
	o.observation.Ready = false
	tick(t, c, 1)
	if s.records[0].State != Delivered {
		t.Fatal("stale healthy state")
	}
	r.snap.Content = manifest("sha256:" + strings.Repeat("c", 64))
	tick(t, c, 1)
	if s.records[0].State != Superseded {
		t.Fatal("newer image not superseding")
	}
}
func TestFailureConflictPRAndRollback(t *testing.T) {
	t.Run("failed source", func(t *testing.T) {
		c, s, r, _ := setup(t)
		s.state = run.Failed
		tick(t, c, 1)
		if s.records[0].State != Cancelled || r.published != 0 {
			t.Fatal(s.records)
		}
	})
	t.Run("conflict", func(t *testing.T) {
		c, s, r, _ := setup(t)
		s.state = run.Succeeded
		r.publishErr = ErrConflict
		tick(t, c, 3)
		if s.records[0].State != Conflict {
			t.Fatal(s.records)
		}
	})
	t.Run("PR merge SHA", func(t *testing.T) {
		c, s, r, _ := setup(t)
		s.state = run.Succeeded
		s.records[0].Target.Mode = "pr"
		tick(t, c, 3)
		if s.records[0].State != AwaitingMerge {
			t.Fatal(s.records)
		}
		tick(t, c, 1)
		r.pr = PullRequest{Merged: true, Closed: true, Revision: strings.Repeat("c", 40)}
		tick(t, c, 1)
		if s.records[0].State != Delivered || s.records[0].Revision != r.pr.Revision {
			t.Fatal(s.records)
		}
	})
	t.Run("PR closed", func(t *testing.T) {
		c, s, r, _ := setup(t)
		s.state = run.Succeeded
		s.records[0].Target.Mode = "pr"
		tick(t, c, 3)
		r.pr = PullRequest{Closed: true}
		tick(t, c, 1)
		if s.records[0].State != Cancelled {
			t.Fatal(s.records)
		}
	})
	t.Run("rollback reverts owned image", func(t *testing.T) {
		c, s, _, _ := setup(t)
		s.state = run.Succeeded
		tick(t, c, 3)
		rollback, err := c.Rollback(t.Context(), "one")
		if err != nil {
			t.Fatal(err)
		}
		if rollback.Digest != "sha256:"+strings.Repeat("a", 64) || rollback.ExpectedDigest != s.records[0].Digest {
			t.Fatal(rollback)
		}
		tick(t, c, 3)
		if s.records[1].State != Delivered {
			t.Fatal(s.records)
		}
		tick(t, c, 1)
		if s.records[0].State != Superseded {
			t.Fatal(s.records)
		}
		retry, err := c.Rollback(t.Context(), "one")
		if err != nil || retry.ID != rollback.ID || len(s.records) != 2 {
			t.Fatal("rollback retry", retry, err)
		}
	})
	t.Run("rollback refuses newer image", func(t *testing.T) {
		c, s, r, _ := setup(t)
		s.state = run.Succeeded
		tick(t, c, 3)
		_, err := c.Rollback(t.Context(), "one")
		if err != nil {
			t.Fatal(err)
		}
		r.snap.Content = manifest("sha256:" + strings.Repeat("c", 64))
		tick(t, c, 1)
		if s.records[1].State != Conflict {
			t.Fatal(s.records)
		}
	})
	t.Run("serialize PR target", func(t *testing.T) {
		c, s, _, _ := setup(t)
		s.state = run.Succeeded
		s.records[0].Target.Mode = "pr"
		other := s.records[0]
		other.ID = "two"
		s.records = append(s.records, other)
		tick(t, c, 4)
		if s.records[1].State != Queued {
			t.Fatal("overlapping PR writes")
		}
	})
}
func TestManifestValidation(t *testing.T) {
	target := testTarget()
	if err := target.Validate(); err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("b", 64)
	for _, source := range []string{"kind: Deployment", string(manifest(digest)) + "---\nkind: Kustomization", strings.Replace(string(manifest(digest)), "name: app", "name: other", 1), "kind: Kustomization\nkind: Kustomization", "kind: Kustomization\nimages: &a [*a]"} {
		if _, _, err := Rewrite([]byte(source), target, digest); err == nil {
			t.Fatalf("accepted %s", source)
		}
	}
	source := strings.Replace(string(manifest(digest)), "    digest:", "    newTag: old\n    digest:", 1)
	out, old, err := Rewrite([]byte(source), target, digest)
	if err != nil || old != digest || strings.Contains(string(out), "newTag") || !strings.Contains(string(out), "deployment.yaml") {
		t.Fatal(string(out), old, err)
	}
	for _, change := range []func(*Target){func(t *Target) { t.Path = "../a.yaml" }, func(t *Target) { t.Branch = "main..x" }, func(t *Target) { t.Mode = "force" }, func(t *Target) { t.Image = "latest" }, func(t *Target) { t.SourceRef = "refs/pull/1/merge" }} {
		bad := target
		change(&bad)
		if err := bad.Validate(); err == nil {
			t.Fatal("accepted invalid target", bad)
		}
	}
}

func TestChangedPRStopsBlockingTargetAndClearsRecoveredErrors(t *testing.T) {
	c, s, r, _ := setup(t)
	s.state = run.Succeeded
	s.records[0].Target.Mode = "pr"
	tick(t, c, 3)
	r.prErr = errors.New("temporary API failure")
	if c.Tick(t.Context()) == nil || s.records[0].Error == "" {
		t.Fatal("lost retry error")
	}
	r.prErr = nil
	tick(t, c, 1)
	if s.records[0].Error != "" {
		t.Fatal("stale error after recovery")
	}
	r.prErr = ErrConflict
	tick(t, c, 1)
	if s.records[0].State != Conflict {
		t.Fatal(s.records)
	}
}

func TestLaterDeliveryOfSameDigestSupersedesOldObservation(t *testing.T) {
	c, s, _, _ := setup(t)
	s.state = run.Succeeded
	tick(t, c, 3)
	other := s.records[0]
	other.ID = "new"
	s.records = append(s.records, other)
	tick(t, c, 1)
	if s.records[0].State != Superseded || s.records[1].State != Delivered {
		t.Fatal(s.records)
	}
}

func TestRemotePollingIsBoundedWithoutDelayingNewDelivery(t *testing.T) {
	c, s, r, o := setup(t)
	now := c.Now()
	c.Now = func() time.Time { return now }
	c.PollInterval = 30 * time.Second
	s.state = run.Succeeded
	tick(t, c, 3)
	o.observation = Observation{Ready: true, Revision: "main@sha1:" + s.records[0].Revision, Image: s.records[0].Target.Image + "@" + s.records[0].Digest}
	tick(t, c, 1)
	reads := r.reads
	tick(t, c, 100)
	if r.reads != reads {
		t.Fatal("unbounded remote polling", r.reads)
	}
	now = now.Add(c.PollInterval)
	tick(t, c, 1)
	if r.reads != reads+1 {
		t.Fatal("poll not resumed")
	}
	now = now.Add(c.PollInterval)
	tick(t, c, 1)
	if r.reads != reads+1 {
		t.Fatal("healthy deployment not polled more slowly")
	}
	other := s.records[0]
	other.ID, other.State, other.Commit = "new", Queued, ""
	s.records = append(s.records, other)
	tick(t, c, 1)
	if s.records[1].State != Preparing {
		t.Fatal("new intent delayed by observation", s.records)
	}
	// A transient publication failure also backs off until the next poll.
	r.publishErr = errors.New("temporary API failure")
	tick(t, c, 1)
	if c.Tick(t.Context()) == nil {
		t.Fatal("expected retryable failure")
	}
	if c.Tick(t.Context()) != nil {
		t.Fatal("immediate repeated remote failure")
	}
	now = now.Add(c.PollInterval)
	r.publishErr = nil
	tick(t, c, 1)
	if s.records[1].State != Delivered {
		t.Fatal(s.records)
	}
}
