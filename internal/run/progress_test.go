package run

import (
	"reflect"
	"testing"
	"time"
)

func TestProgressTransitionsAndRejectedEventsAreImmutable(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	steps := []Step{{ID: "test"}, {ID: "skip"}}
	var p ExecutionProgress
	events := []ProgressEvent{
		{Kind: "phase", Phase: "preparing"},
		{Kind: "phase", Phase: "executing"},
		{Kind: "step", StepID: "test", State: Running},
		{Kind: "annotation", StepID: "test", Level: "warning", Message: "upload not configured"},
		{Kind: "step", StepID: "test", State: Succeeded},
		{Kind: "step", StepID: "skip", State: Skipped},
		{Kind: "phase", Phase: "cleanup"},
	}
	for i, e := range events {
		e.Seq = int64(i + 1)
		e.At = at.Add(time.Duration(i) * time.Second)
		var err error
		p, err = ApplyProgress(p, e, steps)
		if err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
	}
	if p.Phase != "cleanup" || len(p.Steps) != 2 || !p.Phases[0].FinishedAt.Equal(at.Add(time.Second)) || p.Steps[0].FinishedAt.Sub(p.Steps[0].StartedAt) != 2*time.Second {
		t.Fatalf("bad timeline: %+v", p)
	}
	original := p
	for _, e := range []ProgressEvent{
		{Kind: "phase", Phase: "executing"},
		{Kind: "step", StepID: "test", State: Running},
		{Kind: "step", StepID: "alien", State: Succeeded},
		{Kind: "annotation", Level: "warning", Message: ""},
		{Kind: "annotation", Level: "unsupported", Message: "message"},
		{Kind: "unknown"},
	} {
		e.Seq = p.Seq + 1
		e.At = at.Add(10 * time.Second)
		if _, err := ApplyProgress(p, e, steps); err == nil {
			t.Fatalf("accepted %+v", e)
		}
		if !reflect.DeepEqual(p, original) {
			t.Fatal("rejected event changed materialized state")
		}
	}
}

func TestProgressBoundsAndSequence(t *testing.T) {
	at := time.Now().UTC()
	p := ExecutionProgress{Seq: 1, LastAt: at, Phase: "executing"}
	for _, e := range []ProgressEvent{
		{Seq: 1, Kind: "phase", Phase: "cleanup", At: at},
		{Seq: 3, Kind: "phase", Phase: "cleanup", At: at},
		{Seq: 2, Kind: "phase", Phase: "cleanup", At: at.Add(-time.Second)},
		{Seq: 2, Kind: "phase", Phase: "cleanup"},
		{Seq: 2, Kind: "step", StepID: "test", State: Succeeded, At: at},
	} {
		if _, err := ApplyProgress(p, e, []Step{{ID: "test"}}); err == nil {
			t.Fatalf("accepted %+v", e)
		}
	}
	p.Annotations = make([]Annotation, 64)
	if _, err := ApplyProgress(p, ProgressEvent{Seq: 2, Kind: "annotation", Level: "notice", Message: "over limit", At: at}, nil); err == nil {
		t.Fatal("accepted unbounded annotations")
	}
}
