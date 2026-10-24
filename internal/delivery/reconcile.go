package delivery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shitamachi/forgelet/internal/run"
)

type Coordinator struct {
	Store      Store
	Repository Repository
	Observer   Observer
	Now        func() time.Time
	// PollInterval bounds remote reads and retries. Tick is serialized by the
	// control plane; the timer map may reset on restart without replaying writes.
	PollInterval time.Duration
	nextPoll     map[string]time.Time
}

func (c *Coordinator) Tick(ctx context.Context) error {
	records, err := c.Store.Deliveries(ctx, true)
	if err != nil {
		return err
	}
	blocked := map[string]bool{}
	latest := map[string]string{}
	active := map[string]bool{}
	for _, r := range records {
		active[r.ID] = true
		if r.State == Delivered || r.State == Deployed {
			latest[r.Target.Name] = r.ID
		}
	}
	if c.nextPoll == nil {
		c.nextPoll = map[string]time.Time{}
	}
	for id := range c.nextPoll {
		if !active[id] {
			delete(c.nextPoll, id)
		}
	}
	var problems []error
	for _, r := range records {
		if (r.State == Delivered || r.State == Deployed) && latest[r.Target.Name] != r.ID {
			r.State = Superseded
			if err := c.save(ctx, r); err != nil {
				problems = append(problems, err)
			}
			continue
		}
		// Serialize remote writes per registered target until direct delivery or
		// PR resolution. A failed rollout does not block a corrective deployment.
		if blocked[r.Target.Name] && (r.State == Queued || r.State == Preparing) {
			continue
		}
		if r.State == Queued || r.State == Preparing || r.State == AwaitingMerge {
			blocked[r.Target.Name] = true
		}
		if c.Now().Before(c.nextPoll[r.ID]) {
			continue
		}
		interval := c.PollInterval
		if r.State == Deployed {
			interval *= 4
		}
		if r.State == AwaitingMerge || r.State == Delivered || r.State == Deployed {
			c.nextPoll[r.ID] = c.Now().Add(interval)
		}
		if err := c.advance(ctx, r); err != nil {
			c.nextPoll[r.ID] = c.Now().Add(c.PollInterval)
			problems = append(problems, fmt.Errorf("delivery %s: %w", r.ID, err))
			current, readErr := c.Store.GetDelivery(ctx, r.ID)
			if readErr == nil && current.Error != err.Error() {
				current.Error = err.Error()
				if saveErr := c.save(ctx, current); saveErr != nil {
					problems = append(problems, saveErr)
				}
			}
		}
	}
	return errors.Join(problems...)
}
func (c *Coordinator) save(ctx context.Context, r Record) error {
	r.UpdatedAt = c.Now().UTC()
	return c.Store.SaveDelivery(ctx, r)
}
func (c *Coordinator) advance(ctx context.Context, r Record) error {
	previousError := r.Error
	r.Error = ""
	switch r.State {
	case Queued:
		if r.RollbackOf == "" {
			item, err := c.Store.GetRun(ctx, r.RunID)
			if err != nil {
				return err
			}
			if !item.State.Terminal() {
				return nil
			}
			if item.State != run.Succeeded {
				r.State = Cancelled
				r.Error = "source run did not succeed"
				return c.save(ctx, r)
			}
		}
		snap, err := c.Repository.Snapshot(ctx, r.Target, "")
		if err != nil {
			return err
		}
		content, previous, err := Rewrite(snap.Content, r.Target, r.Digest)
		if err != nil {
			return err
		}
		if r.ExpectedDigest != "" && previous != r.ExpectedDigest {
			r.State = Conflict
			r.Error = "rollback would overwrite a newer image"
			return c.save(ctx, r)
		}
		r.Parent = snap.SHA
		r.PreviousDigest = previous
		r.Content = content
		r.State = Preparing
		return c.save(ctx, r)
	case Preparing:
		if r.Commit == "" {
			sha, err := c.Repository.Prepare(ctx, r)
			if err != nil {
				return err
			}
			if !shaPattern.MatchString(sha) {
				return fmt.Errorf("invalid GitOps commit")
			}
			r.Commit = sha
			return c.save(ctx, r)
		}
		pub, err := c.Repository.Publish(ctx, r)
		if errors.Is(err, ErrConflict) {
			r.State = Conflict
			r.Error = "GitOps branch changed; retry with a new run"
			return c.save(ctx, r)
		}
		if err != nil {
			return err
		}
		r.PR = pub.PR
		r.URL = pub.URL
		r.Revision = pub.Revision
		if r.PR > 0 {
			r.State = AwaitingMerge
		} else {
			if !shaPattern.MatchString(r.Revision) {
				return fmt.Errorf("invalid published revision")
			}
			r.State = Delivered
		}
		return c.save(ctx, r)
	case AwaitingMerge:
		pr, err := c.Repository.PullRequest(ctx, r)
		if errors.Is(err, ErrConflict) {
			r.State = Conflict
			r.Error = "delivery PR head or base changed"
			return c.save(ctx, r)
		}
		if err != nil {
			return err
		}
		if pr.Merged {
			if !shaPattern.MatchString(pr.Revision) {
				return fmt.Errorf("merged PR has no valid revision")
			}
			r.Revision = pr.Revision
			r.State = Delivered
			return c.save(ctx, r)
		}
		if pr.Closed {
			r.State = Cancelled
			r.Error = "delivery PR closed without merge"
			return c.save(ctx, r)
		}
	case Delivered, Deployed:
		// Verify target branch still contains the actual merge SHA. A later image
		// update supersedes this record even if Flux has not caught up yet.
		head, err := c.Repository.Snapshot(ctx, r.Target, "")
		if err != nil {
			return err
		}
		_, current, err := Rewrite(head.Content, r.Target, r.Digest)
		if err != nil {
			return err
		}
		contains, err := c.Repository.Contains(ctx, r.Target, r.Revision, head.SHA)
		if err != nil {
			return err
		}
		if !contains || current != r.Digest {
			r.State = Superseded
			return c.save(ctx, r)
		}
		observation, err := c.Observer.Observe(ctx, r.Target)
		if err != nil {
			return err
		}
		observed := RevisionSHA(observation.Revision)
		ready := observation.Ready && observed == head.SHA && observation.Image == r.Target.Image+"@"+r.Digest
		next := Delivered
		if ready {
			next = Deployed
		}
		if next != r.State || observed != r.ObservedRevision {
			r.State = next
			r.ObservedRevision = observed
			return c.save(ctx, r)
		}
	}
	if previousError != "" {
		return c.save(ctx, r)
	}
	return nil
}

func (c *Coordinator) Rollback(ctx context.Context, id string) (Record, error) {
	// An acknowledged or in-flight rollback remains the same operation even
	// after it has superseded the original deployment.
	prior, err := c.Store.GetDelivery(ctx, ID("rollback", id))
	if err == nil {
		return prior, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Record{}, err
	}
	original, err := c.Store.GetDelivery(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if (original.State != Delivered && original.State != Deployed) || !run.ValidDigest(original.PreviousDigest) {
		return Record{}, fmt.Errorf("rollback requires a delivered image with a recorded previous digest: %w", ErrConflict)
	}
	now := c.Now().UTC()
	r := Record{ID: ID("rollback", id), Target: original.Target, RunID: original.RunID, JobID: original.JobID, Digest: original.PreviousDigest, ExpectedDigest: original.Digest, RollbackOf: original.ID, State: Queued, CreatedAt: now, UpdatedAt: now}
	return c.Store.EnqueueDelivery(ctx, r)
}
