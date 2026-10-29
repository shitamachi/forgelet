package actworker

import (
	"fmt"

	"github.com/sirupsen/logrus"

	"github.com/shitamachi/forgelet/internal/run"
)

// act already supplies stage, root/composite step IDs and stepResult fields.
// No displayed log message or action-specific success text is parsed here.
type workerProgress struct {
	started  map[string]bool
	finished map[string]bool
}

func (p *workerProgress) event(e *logrus.Entry) *run.ProgressEvent {
	ids, _ := e.Data["stepID"].([]string)
	command, _ := e.Data["command"].(string)
	if command == "warning" || command == "error" || command == "notice" {
		text, _ := e.Data["arg"].(string)
		if text != "" {
			id := ""
			if len(ids) > 0 {
				id = ids[0]
			}
			return &run.ProgressEvent{Kind: "annotation", StepID: id, Level: command, Message: text}
		}
	}
	if len(ids) != 1 || e.Data["stage"] != "Main" {
		return nil
	}
	id := ids[0]
	if outcome, ok := e.Data["stepResult"]; ok {
		state := run.State(fmt.Sprint(outcome))
		if state != run.Succeeded && state != run.Failed && state != run.Skipped {
			return nil
		}
		if p.finished[id] {
			return nil
		}
		p.finished[id] = true
		return &run.ProgressEvent{Kind: "step", StepID: id, State: state}
	}
	if e.Level <= logrus.InfoLevel && !p.started[id] && !p.finished[id] {
		p.started[id] = true
		return &run.ProgressEvent{Kind: "step", StepID: id, State: run.Running}
	}
	return nil
}
