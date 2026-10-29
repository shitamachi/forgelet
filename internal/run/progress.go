package run

import (
	"fmt"
	"time"
	"unicode/utf8"
)

const MaxProgressEvents = 2048

type ProgressEvent struct {
	Seq     int64     `json:"seq"`
	Kind    string    `json:"kind"`
	At      time.Time `json:"at"`
	Phase   string    `json:"phase,omitempty"`
	StepID  string    `json:"step_id,omitempty"`
	State   State     `json:"state,omitempty"`
	Level   string    `json:"level,omitempty"`
	Message string    `json:"message,omitempty"`
}

type PhaseProgress struct {
	Name       string    `json:"name"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
}

type StepProgress struct {
	ID         string    `json:"id"`
	State      State     `json:"state"`
	StartedAt  time.Time `json:"started_at,omitzero"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
}

type Annotation struct {
	StepID  string    `json:"step_id,omitempty"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}

// ExecutionProgress describes observations, never the authoritative job result.
// It contains no outputs, secrets or arbitrary worker-provided contexts.
type ExecutionProgress struct {
	Seq         int64           `json:"seq"`
	Phase       string          `json:"phase"`
	LastAt      time.Time       `json:"last_at"`
	Phases      []PhaseProgress `json:"phases,omitempty"`
	Steps       []StepProgress  `json:"steps,omitempty"`
	Annotations []Annotation    `json:"annotations,omitempty"`
}

// ApplyProgress is pure and returns a copy; rejected events cannot mutate state.
func ApplyProgress(old ExecutionProgress, e ProgressEvent, steps []Step) (ExecutionProgress, error) {
	fail := func() (ExecutionProgress, error) { return old, fmt.Errorf("invalid or out-of-order progress event") }
	if e.Seq != old.Seq+1 || e.Seq > MaxProgressEvents || e.At.IsZero() || e.At.Before(old.LastAt) || len(e.Message) > 1024 || !utf8.ValidString(e.Message) {
		return fail()
	}
	known := e.StepID == ""
	for _, step := range steps {
		known = known || step.ID == e.StepID
	}
	if !known {
		return fail()
	}
	next := old
	next.Phases = append([]PhaseProgress(nil), old.Phases...)
	next.Steps = append([]StepProgress(nil), old.Steps...)
	next.Annotations = append([]Annotation(nil), old.Annotations...)
	switch e.Kind {
	case "phase":
		rank := map[string]int{"preparing": 1, "executing": 2, "cleanup": 3}
		if rank[e.Phase] == 0 || rank[e.Phase] <= rank[old.Phase] || e.StepID != "" || e.State != "" || e.Message != "" || e.Level != "" {
			return fail()
		}
		if len(next.Phases) > 0 {
			next.Phases[len(next.Phases)-1].FinishedAt = e.At
		}
		next.Phase = e.Phase
		next.Phases = append(next.Phases, PhaseProgress{Name: e.Phase, StartedAt: e.At})
	case "step":
		if e.StepID == "" || old.Phase != "executing" || e.Phase != "" || e.Message != "" || e.Level != "" {
			return fail()
		}
		index := -1
		for i, s := range next.Steps {
			if s.ID == e.StepID {
				index = i
			}
		}
		if e.State == Running {
			if index >= 0 {
				return fail()
			}
			next.Steps = append(next.Steps, StepProgress{ID: e.StepID, State: Running, StartedAt: e.At})
		} else {
			if e.State != Succeeded && e.State != Failed && e.State != Cancelled && e.State != TimedOut && e.State != Skipped {
				return fail()
			}
			if index < 0 {
				if e.State != Skipped {
					return fail()
				}
				next.Steps = append(next.Steps, StepProgress{ID: e.StepID, State: Skipped, FinishedAt: e.At})
			} else {
				if next.Steps[index].State != Running {
					return fail()
				}
				next.Steps[index].State = e.State
				next.Steps[index].FinishedAt = e.At
			}
		}
	case "annotation":
		if e.Level != "notice" && e.Level != "warning" && e.Level != "error" {
			return fail()
		}
		if e.Message == "" || len(next.Annotations) >= 64 || e.State != "" || e.Phase != "" {
			return fail()
		}
		next.Annotations = append(next.Annotations, Annotation{StepID: e.StepID, Level: e.Level, Message: e.Message, At: e.At})
	default:
		return fail()
	}
	next.Seq, next.LastAt = e.Seq, e.At
	return next, nil
}
