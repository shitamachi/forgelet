// Package memory provides in-memory adapters for the scheduler ports. The
// durable store mimics PostgreSQL semantics — unique keys, atomic
// create-or-get, serialized claims, monotonic projection — so protocol tests
// written against it transfer to the PostgreSQL adapter.
package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/run/scheduler"
)

// compile-time interface checks (adapters may depend on the application layer).
var (
	_ scheduler.DurableStore         = (*DurableStore)(nil)
	_ scheduler.ActiveExecutionStore = (*ActiveStore)(nil)
)

// Clock is the injectable time source.
type Clock func() time.Time

// DurableStore is the in-memory DurableStore implementation.
type DurableStore struct {
	mu         sync.Mutex
	clock      Clock
	ids        *scheduler.IDGen
	deliveries map[model.DeliveryKey]model.DeliveryRecord
	runs       map[model.RunID]model.RunRecord
	jobs       map[model.JobRunID]model.JobRunRecord
	runJobs    map[model.RunID][]model.JobRunID
	byJobKey   map[model.RunID]map[string]model.JobRunID
	// claimed holds job IDs currently handed out by ClaimNextQueuedJob and
	// not yet terminal/dispatched; it emulates a row lock.
	claimed map[model.JobRunID]bool
}

// NewDurableStore creates a store. clock defaults to time.Now.
func NewDurableStore(clock Clock, ids *scheduler.IDGen) *DurableStore {
	if clock == nil {
		clock = time.Now
	}
	if ids == nil {
		ids = scheduler.NewIDGen(scheduler.Clock(clock), nil)
	}
	return &DurableStore{
		clock:      clock,
		ids:        ids,
		deliveries: map[model.DeliveryKey]model.DeliveryRecord{},
		runs:       map[model.RunID]model.RunRecord{},
		jobs:       map[model.JobRunID]model.JobRunRecord{},
		runJobs:    map[model.RunID][]model.JobRunID{},
		byJobKey:   map[model.RunID]map[string]model.JobRunID{},
		claimed:    map[model.JobRunID]bool{},
	}
}

// RecordDelivery implements scheduler.DurableStore.
func (s *DurableStore) RecordDelivery(_ context.Context, d model.Delivery) (model.DeliveryRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec, ok := s.deliveries[d.Key]; ok {
		return rec, false, nil
	}
	rec := model.DeliveryRecord{Key: d.Key, Event: d.Event, Payload: append([]byte(nil), d.Payload...), ReceivedAt: s.clock()}
	s.deliveries[d.Key] = rec
	return rec, true, nil
}

// CreateRun implements scheduler.DurableStore: atomic run+jobs creation,
// create-or-get keyed by delivery.
func (s *DurableStore) CreateRun(_ context.Context, seed model.RunSeed, now time.Time) (model.RunRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if rec, ok := s.deliveries[seed.Delivery]; ok && rec.RunID != "" {
		run, rerr := s.getRunLocked(rec.RunID)
		if rerr != nil {
			return model.RunRecord{}, false, rerr
		}
		return run, false, nil
	}
	if _, ok := s.deliveries[seed.Delivery]; !ok {
		return model.RunRecord{}, false, fmt.Errorf("memory: delivery %s not recorded", seed.Delivery)
	}

	runID := s.ids.NewRunID()
	run := model.RunRecord{ID: runID, Event: seed.Event, Delivery: seed.Delivery, Status: model.RunQueued, CreatedAt: now}

	keyIndex := map[string]model.JobRunID{}
	jobIDs := make([]model.JobRunID, 0, len(seed.Jobs))
	for _, intent := range seed.Jobs {
		if _, dup := keyIndex[intent.JobKey]; dup {
			return model.RunRecord{}, false, fmt.Errorf("memory: duplicate job key %q in run %s", intent.JobKey, runID)
		}
		jobID := s.ids.NewJobRunID()
		job := model.JobRunRecord{
			ID:          jobID,
			RunID:       runID,
			JobKey:      intent.JobKey,
			RunnerClass: intent.RunnerClass,
			PlanDigest:  intent.PlanDigest,
			Status:      model.JobQueued,
			Attempt:     1,
			CreatedAt:   now,
		}
		s.jobs[jobID] = job
		keyIndex[intent.JobKey] = jobID
		jobIDs = append(jobIDs, jobID)
	}

	// Commit: all writes below are visible together under the store lock.
	s.runs[runID] = run
	s.runJobs[runID] = jobIDs
	s.byJobKey[runID] = keyIndex
	rec := s.deliveries[seed.Delivery]
	rec.RunID = runID
	s.deliveries[seed.Delivery] = rec
	return run, true, nil
}

// GetRun implements scheduler.DurableStore.
func (s *DurableStore) GetRun(_ context.Context, id model.RunID) (model.RunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getRunLocked(id)
}

func (s *DurableStore) getRunLocked(id model.RunID) (model.RunRecord, error) {
	run, ok := s.runs[id]
	if !ok {
		return model.RunRecord{}, scheduler.ErrRunNotFound
	}
	return run, nil
}

// ListJobRuns implements scheduler.DurableStore.
func (s *DurableStore) ListJobRuns(_ context.Context, run model.RunID) ([]model.JobRunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.runs[run]; !ok {
		return nil, scheduler.ErrRunNotFound
	}
	ids := append([]model.JobRunID(nil), s.runJobs[run]...)
	out := make([]model.JobRunRecord, 0, len(ids))
	for _, id := range ids {
		out = append(out, s.jobs[id])
	}
	return out, nil
}

// GetJobRun returns a single job record (test/dev convenience).
func (s *DurableStore) GetJobRun(_ context.Context, id model.JobRunID) (model.JobRunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return model.JobRunRecord{}, scheduler.ErrJobRunNotFound
	}
	return job, nil
}

// ClaimNextQueuedJob implements scheduler.DurableStore: returns the oldest
// queued, unclaimed job and marks it claimed. Claims are released by
// AckDispatched, ApplyObserved reaching terminal, or CancelRun.
func (s *DurableStore) ClaimNextQueuedJob(context.Context) (model.JobRunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	type candidate struct {
		id model.JobRunID
		at time.Time
	}
	var candidates []candidate
	for id, job := range s.jobs {
		if job.Status == model.JobQueued && !s.claimed[id] {
			candidates = append(candidates, candidate{id: id, at: job.CreatedAt})
		}
	}
	if len(candidates) == 0 {
		return model.JobRunRecord{}, scheduler.ErrNoQueuedJob
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].at.Equal(candidates[j].at) {
			return candidates[i].id < candidates[j].id
		}
		return candidates[i].at.Before(candidates[j].at)
	})
	pick := candidates[0]
	s.claimed[pick.id] = true
	return s.jobs[pick.id], nil
}

// ReleaseClaim implements scheduler.DurableStore.
func (s *DurableStore) ReleaseClaim(_ context.Context, id model.JobRunID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.claimed, id)
	return nil
}

// AckDispatched implements scheduler.DurableStore.
func (s *DurableStore) AckDispatched(_ context.Context, id model.JobRunID, obj scheduler.ActiveObject, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return scheduler.ErrJobRunNotFound
	}
	switch job.Status {
	case model.JobDispatched:
		if job.ActiveName != "" && job.ActiveName != obj.Name {
			return fmt.Errorf("memory: %s already dispatched as %s, refusing %s", id, job.ActiveName, obj.Name)
		}
	case model.JobQueued:
		job.Status = model.JobDispatched
	default:
		return fmt.Errorf("%w: %s in status %s", scheduler.ErrJobNotDispatchable, id, job.Status)
	}
	job.ActiveName = obj.Name
	job.ActiveUID = obj.UID
	t := now
	job.DispatchedAt = &t
	s.jobs[id] = job
	delete(s.claimed, id)
	return nil
}

// ApplyObserved implements scheduler.DurableStore: monotonic projection.
func (s *DurableStore) ApplyObserved(_ context.Context, id model.JobRunID, phase model.ObservedPhase, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[id]
	if !ok {
		return scheduler.ErrJobRunNotFound
	}
	if job.Status.IsTerminal() {
		return nil // terminal is sticky; stale observation ignored
	}
	target, changes := model.JobStatusFromPhase(phase)
	if changes {
		if !model.CanAdvanceJob(job.Status, target) {
			return nil // stale or non-forward observation ignored
		}
		job.Status = target
		switch target {
		case model.JobRunning:
			t := now
			job.StartedAt = &t
		case model.JobSucceeded, model.JobFailed:
			t := now
			job.StartedAt = orTime(job.StartedAt, t)
			job.FinishedAt = &t
		}
		s.jobs[id] = job
	}
	s.refreshRunLocked(job.RunID, now)
	return nil
}

// CancelRun implements scheduler.DurableStore.
func (s *DurableStore) CancelRun(_ context.Context, id model.RunID, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[id]
	if !ok {
		return scheduler.ErrRunNotFound
	}
	if run.Status.IsTerminal() {
		if run.Status == model.RunCancelled {
			return nil
		}
		return fmt.Errorf("memory: run %s already terminal (%s)", id, run.Status)
	}
	for _, jobID := range s.runJobs[id] {
		job := s.jobs[jobID]
		if next, err := model.TransitionJob(job.Status, model.JobCancelled); err == nil {
			job.Status = next
			t := now
			job.FinishedAt = &t
			s.jobs[jobID] = job
		}
		delete(s.claimed, jobID)
	}
	t := now
	run.Status = model.RunCancelled
	run.FinishedAt = &t
	s.runs[id] = run
	return nil
}

// MarkCollected implements scheduler.DurableStore.
func (s *DurableStore) MarkCollected(_ context.Context, id model.JobRunID, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return scheduler.ErrJobRunNotFound
	}
	if job.ActiveCollectedAt == nil {
		t := now
		job.ActiveCollectedAt = &t
		s.jobs[id] = job
	}
	return nil
}

// Runs returns a copy of all run records (test convenience).
func (s *DurableStore) Runs() []model.RunRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]model.RunRecord, 0, len(s.runs))
	for _, id := range s.sortedRunIDsLocked() {
		out = append(out, s.runs[id])
	}
	return out
}

// ListGCReadyJobs implements scheduler.DurableStore.
func (s *DurableStore) ListGCReadyJobs(context.Context) ([]model.JobRunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.JobRunRecord
	for _, id := range s.sortedRunIDsLocked() {
		run := s.runs[id]
		if !run.Status.IsTerminal() {
			continue
		}
		for _, jobID := range s.runJobs[id] {
			job := s.jobs[jobID]
			if job.Status.IsTerminal() && job.ActiveCollectedAt == nil {
				out = append(out, job)
			}
		}
	}
	return out, nil
}

func (s *DurableStore) sortedRunIDsLocked() []model.RunID {
	ids := make([]model.RunID, 0, len(s.runs))
	for id := range s.runs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// refreshRunLocked recomputes the run aggregate monotonically.
func (s *DurableStore) refreshRunLocked(runID model.RunID, now time.Time) {
	run, ok := s.runs[runID]
	if !ok || run.Status.IsTerminal() {
		return
	}
	statuses := make([]model.JobRunStatus, 0, len(s.runJobs[runID]))
	for _, jobID := range s.runJobs[runID] {
		statuses = append(statuses, s.jobs[jobID].Status)
	}
	next := model.AggregateRunStatus(statuses)
	if model.CanAdvanceRun(run.Status, next) && next != run.Status {
		run.Status = next
		if next.IsTerminal() {
			t := now
			run.FinishedAt = &t
		}
		s.runs[runID] = run
	}
}

func orTime(t *time.Time, fallback time.Time) *time.Time {
	if t != nil {
		return t
	}
	return &fallback
}

// ActiveStore is the in-memory ActiveExecutionStore. It simulates the
// deterministic create-or-get behaviour of the future CRD controller and
// counts creates/deletes for protocol assertions.
type ActiveStore struct {
	mu      sync.Mutex
	objects map[model.JobRunID]scheduler.ActiveObject
	uids    map[string]string

	CreateCalls int
	DeleteCalls int
	// Created counts objects actually inserted (calls that found an
	// existing object do not count). Protocol tests assert on this.
	Created int
}

// NewActiveStore creates an ActiveStore.
func NewActiveStore() *ActiveStore {
	return &ActiveStore{objects: map[model.JobRunID]scheduler.ActiveObject{}, uids: map[string]string{}}
}

// CreateOrGet implements scheduler.ActiveExecutionStore.
func (a *ActiveStore) CreateOrGet(_ context.Context, id model.JobRunID) (scheduler.ActiveObject, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.CreateCalls++
	if obj, ok := a.objects[id]; ok {
		return obj, nil
	}
	a.Created++
	name := id.CRName()
	uid := a.uids[name]
	if uid == "" {
		uid = fmt.Sprintf("uid-%s", name)
		a.uids[name] = uid
	}
	obj := scheduler.ActiveObject{Name: name, UID: uid}
	a.objects[id] = obj
	return obj, nil
}

// Delete implements scheduler.ActiveExecutionStore; missing objects succeed.
func (a *ActiveStore) Delete(_ context.Context, id model.JobRunID) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.DeleteCalls++
	delete(a.objects, id)
	return nil
}

// Objects returns a copy of the current objects (test convenience).
func (a *ActiveStore) Objects() map[model.JobRunID]scheduler.ActiveObject {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[model.JobRunID]scheduler.ActiveObject, len(a.objects))
	for k, v := range a.objects {
		out[k] = v
	}
	return out
}
