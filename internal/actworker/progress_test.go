package actworker

import (
	"testing"

	"github.com/nektos/act/pkg/model"
	"github.com/sirupsen/logrus"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestStructuredActProgressDoesNotParseDisplayedMessages(t *testing.T) {
	p := workerProgress{started: map[string]bool{}, finished: map[string]bool{}}
	entry := func(ids []string, fields logrus.Fields) *logrus.Entry {
		fields["stepID"] = ids
		fields["stage"] = "Main"
		return &logrus.Entry{Level: logrus.InfoLevel, Message: "arbitrary translated output", Data: fields}
	}
	start := p.event(entry([]string{"build"}, logrus.Fields{}))
	if start == nil || start.State != run.Running {
		t.Fatal(start)
	}
	if e := p.event(entry([]string{"build", "nested"}, logrus.Fields{"stepResult": model.StepStatusSuccess})); e != nil {
		t.Fatalf("nested action finished root: %+v", e)
	}
	end := p.event(entry([]string{"build"}, logrus.Fields{"stepResult": model.StepStatusFailure, "executionTime": 123}))
	if end == nil || end.State != run.Failed {
		t.Fatal(end)
	}
	if e := p.event(entry([]string{"build"}, logrus.Fields{})); e != nil {
		t.Fatal("background output restarted completed step")
	}
	skip := p.event(entry([]string{"skip"}, logrus.Fields{"stepResult": model.StepStatusSkipped}))
	if skip == nil || skip.State != run.Skipped {
		t.Fatal(skip)
	}
	annotation := p.event(entry([]string{"build", "nested"}, logrus.Fields{"command": "warning", "arg": "upload was rejected"}))
	if annotation == nil || annotation.Kind != "annotation" || annotation.StepID != "build" || annotation.Message != "upload was rejected" {
		t.Fatal(annotation)
	}
	// A matching-looking line without the structured fields cannot complete a step.
	if e := p.event(&logrus.Entry{Message: "Success - Main build [1s]", Data: logrus.Fields{}}); e != nil {
		t.Fatal(e)
	}
}
