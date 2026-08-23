// Package schedule implements forgelet's internal cron scheduler (spec 0001
// FR-1.4): workflows' `on.schedule` entries fire without any GitHub webhook,
// deduplicated by (repository, workflow, cron expression, fire time).
package schedule

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/shitamachi/forgelet/internal/run/model"
)

// ScheduledWorkflow is one (repo, workflow, cron) triple discovered on a
// repository's default branch.
type ScheduledWorkflow struct {
	Repository model.RepositoryRef
	FileName   string // workflow file identity for the idempotency key
	Ref        string // default branch the schedule was read from
	Cron       string
}

// Lister discovers scheduled workflows (adapter: server, reading the
// default branch through the workflow source).
type Lister interface {
	List(ctx context.Context) ([]ScheduledWorkflow, error)
}

// IngestPort is satisfied by the scheduler.Ingestor.
type IngestPort interface {
	Ingest(ctx context.Context, d model.Delivery) (model.RunID, bool, error)
}

// Scheduler fires cron schedules. Semantics (spec 0002 T9):
//   - idempotency key = provider "forgelet" +
//     schedule:{repo}:{file}:{cron}:{fire time UTC}; replays dedupe;
//   - missed fires while the control plane was down emit only the most
//     recent one (GitHub-like behavior, documented choice);
//   - overlapping suppression is implied: one run per fire time.
type Scheduler struct {
	List     Lister
	Ingest   IngestPort
	Location *time.Location
	Now      func() time.Time

	mu    sync.Mutex
	fired map[string]time.Time
}

// New wires a Scheduler (UTC by default).
func New(list Lister, ingest IngestPort, now func() time.Time) *Scheduler {
	if now == nil {
		now = time.Now
	}
	return &Scheduler{List: list, Ingest: ingest, Location: time.UTC, Now: now,
		fired: map[string]time.Time{}}
}

var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// Tick lists schedules and emits deliveries due at or before now. It
// returns how many deliveries were emitted.
func (s *Scheduler) Tick(ctx context.Context) (int, error) {
	workflows, err := s.List.List(ctx)
	if err != nil {
		return 0, fmt.Errorf("schedule: list: %w", err)
	}
	now := s.Now().In(s.Location)
	emitted := 0

	for _, sw := range workflows {
		sched, err := parser.Parse(sw.Cron)
		if err != nil {
			continue // invalid expressions are reported by the workflow compiler
		}
		key := scheduleKey(sw)

		s.mu.Lock()
		lastEmitted := s.fired[key]
		s.mu.Unlock()

		// Walk forward from the last emitted fire; without history (restart)
		// the recovery window is 24h — older fires are skipped entirely and
		// only the latest missed fire inside the window is emitted. The cap
		// keeps the walk bounded for minute-grain crons.
		cursor := lastEmitted
		if cursor.IsZero() {
			cursor = now.Add(-24 * time.Hour)
		}
		var latest time.Time
		for {
			n := sched.Next(cursor)
			if n.IsZero() || n.After(now) {
				break
			}
			latest = n
			cursor = n
		}
		if latest.IsZero() || latest.Equal(lastEmitted) {
			continue
		}

		delivery := model.Delivery{
			Key: model.DeliveryKey{
				Provider: "forgelet",
				DeliveryID: fmt.Sprintf("schedule:%s/%s:%s:%s:%s",
					sw.Repository.Owner, sw.Repository.Name, sw.FileName, sw.Cron,
					latest.UTC().Format(time.RFC3339)),
			},
			Event: model.Event{
				Provider:   "forgelet",
				Name:       "schedule",
				DeliveryID: fmt.Sprintf("%s/%s@%s", sw.Repository.Owner, sw.Repository.Name, sw.FileName),
				Repository: sw.Repository,
				Ref:        sw.Ref,
				Actor:      "forgelet-schedule",
			},
			Payload: []byte(fmt.Sprintf(`{"cron":%q,"fire_time":%q,"workflow":%q}`,
				sw.Cron, latest.UTC().Format(time.RFC3339), sw.FileName)),
		}
		if _, _, err := s.Ingest.Ingest(ctx, delivery); err != nil {
			return emitted, fmt.Errorf("schedule: ingest %s: %w", delivery.Key, err)
		}
		s.mu.Lock()
		s.fired[key] = latest
		s.mu.Unlock()
		emitted++
	}
	return emitted, nil
}

func scheduleKey(sw ScheduledWorkflow) string {
	return fmt.Sprintf("%s/%s:%s:%s", sw.Repository.Owner, sw.Repository.Name, sw.FileName, sw.Cron)
}
