package schedule

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run/model"
)

type fakeLister struct{ items []ScheduledWorkflow }

func (f fakeLister) List(context.Context) ([]ScheduledWorkflow, error) { return f.items, nil }

type fakeIngest struct {
	got  []model.Delivery
	fail error
}

func (f *fakeIngest) Ingest(_ context.Context, d model.Delivery) (model.RunID, bool, error) {
	f.got = append(f.got, d)
	if f.fail != nil {
		return "", false, f.fail
	}
	return model.RunID("01JR"), true, nil
}

func at(minute int) time.Time {
	return time.Date(2026, 8, 23, 12, minute, 0, 0, time.UTC)
}

func repo() model.RepositoryRef {
	return model.RepositoryRef{Provider: "github", Owner: "o", Name: "r"}
}

func TestTickFiresDueSchedules(t *testing.T) {
	list := fakeLister{[]ScheduledWorkflow{{
		Repository: repo(), FileName: "ci.yml", Ref: "refs/heads/main", Cron: "*/5 * * * *",
	}}}
	ing := &fakeIngest{}
	s := New(list, ing, nil)
	s.Now = func() time.Time { return at(0) }
	ctx := context.Background()

	// 12:00 — the 12:00 fire itself is due (fresh start scans a bounded
	// 24h window and lands on the latest fire at or before now).
	if n, err := s.Tick(ctx); err != nil || n != 1 {
		t.Fatalf("tick at :00: %d %v", n, err)
	}
	fire := ing.got[0]
	if fire.Key.Provider != "forgelet" {
		t.Errorf("provider = %q", fire.Key.Provider)
	}
	if want := "schedule:o/r:ci.yml:*/5 * * * *:2026-08-23T12:00:00Z"; fire.Key.DeliveryID != want {
		t.Errorf("idempotency key = %q, want %q", fire.Key.DeliveryID, want)
	}
	// 12:05 — next fire.
	s.Now = func() time.Time { return at(5) }
	if n, err := s.Tick(ctx); err != nil || n != 1 {
		t.Fatalf("tick at :05: %d %v", n, err)
	}
	if fire.Event.Name != "schedule" || fire.Event.Ref != "refs/heads/main" {
		t.Errorf("event = %+v", fire.Event)
	}
	// 12:06 — same fire time deduped in-memory.
	s.Now = func() time.Time { return at(6) }
	if n, err := s.Tick(ctx); err != nil || n != 0 {
		t.Fatalf("tick at :06: %d %v", n, err)
	}
	// 12:11 — fires :10 (missed :05 already emitted).
	s.Now = func() time.Time { return at(11) }
	if n, err := s.Tick(ctx); err != nil || n != 1 {
		t.Fatalf("tick at :11: %d %v", n, err)
	}
	if got := ing.got[len(ing.got)-1].Key.DeliveryID; got != "schedule:o/r:ci.yml:*/5 * * * *:2026-08-23T12:10:00Z" {
		t.Errorf("second fire = %q", got)
	}
}

// Missed fires while the control plane was down emit only the latest one.
func TestTickMissedFiresEmitLatestOnly(t *testing.T) {
	list := fakeLister{[]ScheduledWorkflow{{
		Repository: repo(), FileName: "ci.yml", Ref: "refs/heads/main", Cron: "*/5 * * * *",
	}}}
	ing := &fakeIngest{}
	s := New(list, ing, nil)
	s.Now = func() time.Time { return at(57) } // missed :05..:55 while down
	n, err := s.Tick(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("tick: %d %v", n, err)
	}
	want := "schedule:o/r:ci.yml:*/5 * * * *:2026-08-23T12:55:00Z"
	if got := ing.got[0].Key.DeliveryID; got != want {
		t.Errorf("fire = %q, want latest %q", got, want)
	}
}

func TestTickMultipleSchedulesIndependent(t *testing.T) {
	list := fakeLister{[]ScheduledWorkflow{
		{Repository: repo(), FileName: "a.yml", Ref: "refs/heads/main", Cron: "*/10 * * * *"},
		{Repository: repo(), FileName: "a.yml", Ref: "refs/heads/main", Cron: "*/15 * * * *"},
	}}
	ing := &fakeIngest{}
	s := New(list, ing, nil)
	s.Now = func() time.Time { return at(30) }
	if n, err := s.Tick(context.Background()); err != nil || n != 2 {
		t.Fatalf("tick: %d %v", n, err)
	}
	keys := map[string]bool{}
	for _, d := range ing.got {
		keys[d.Key.DeliveryID] = true
	}
	// 12:30 fires both */10 (12:30) and */15 (12:30).
	if len(keys) != 2 {
		t.Errorf("keys = %v", keys)
	}
}

func TestTickInvalidCronSkipped(t *testing.T) {
	list := fakeLister{[]ScheduledWorkflow{{
		Repository: repo(), FileName: "bad.yml", Ref: "refs/heads/main", Cron: "not a cron",
	}}}
	ing := &fakeIngest{}
	s := New(list, ing, nil)
	s.Now = func() time.Time { return at(30) }
	if n, err := s.Tick(context.Background()); err != nil || n != 0 {
		t.Fatalf("tick: %d %v", n, err)
	}
}

func TestTickIngestErrorPropagates(t *testing.T) {
	list := fakeLister{[]ScheduledWorkflow{{
		Repository: repo(), FileName: "ci.yml", Ref: "refs/heads/main", Cron: "* * * * *",
	}}}
	ing := &fakeIngest{fail: errors.New("injected")}
	s := New(list, ing, nil)
	s.Now = func() time.Time { return at(62) }
	if _, err := s.Tick(context.Background()); err == nil {
		t.Fatal("ingest error must propagate")
	}
}
