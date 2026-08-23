package report

import (
	"strings"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run/model"
)

func job(status model.JobRunStatus) model.JobRunRecord {
	started := time.Unix(1_700_000_100, 0).UTC()
	finished := time.Unix(1_700_000_200, 0).UTC()
	return model.JobRunRecord{
		ID:         model.JobRunID("01JJOB000000000000000000A"),
		RunID:      model.RunID("01JRUN000000000000000000X"),
		JobKey:     "test",
		Status:     status,
		StartedAt:  &started,
		FinishedAt: &finished,
	}
}

func TestMapJobRunMatrix(t *testing.T) {
	cases := []struct {
		status     model.JobRunStatus
		wantStatus CheckStatus
		wantConcl  CheckConclusion
	}{
		{model.JobQueued, StatusQueued, ""},
		{model.JobDispatched, StatusProgress, ""},
		{model.JobRunning, StatusProgress, ""},
		{model.JobSucceeded, StatusCompleted, ConclusionSuccess},
		{model.JobFailed, StatusCompleted, ConclusionFailure},
		{model.JobCancelled, StatusCompleted, ConclusionCancelled},
	}
	for _, tc := range cases {
		c, err := MapJobRun(job(tc.status), "https://ci.example.com")
		if err != nil {
			t.Fatalf("%s: %v", tc.status, err)
		}
		if c.Status != tc.wantStatus || c.Conclusion != tc.wantConcl {
			t.Errorf("%s: got (%s,%s), want (%s,%s)", tc.status, c.Status, c.Conclusion, tc.wantStatus, tc.wantConcl)
		}
		if c.ExternalID != "01JJOB000000000000000000A" {
			t.Errorf("%s: external id = %s", tc.status, c.ExternalID)
		}
		if !strings.HasPrefix(c.DetailsURL, "https://ci.example.com/runs/01JRUN000000000000000000X/jobs/test") {
			t.Errorf("%s: details url = %s", tc.status, c.DetailsURL)
		}
	}

	// Terminal states carry completed_at.
	c, _ := MapJobRun(job(model.JobSucceeded), "https://ci.example.com")
	if c.CompletedAt == nil {
		t.Error("succeeded must map FinishedAt to CompletedAt")
	}
	// Non-terminal states don't.
	c, _ = MapJobRun(job(model.JobRunning), "https://ci.example.com")
	if c.CompletedAt != nil {
		t.Error("running must not carry a conclusion timestamp")
	}
}

func TestMapJobRunErrors(t *testing.T) {
	empty := job(model.JobQueued)
	empty.JobKey = ""
	if _, err := MapJobRun(empty, "https://x"); err == nil {
		t.Error("empty job key must fail")
	}
	bad := job(model.JobRunStatus("weird"))
	if _, err := MapJobRun(bad, "https://x"); err == nil || !strings.Contains(err.Error(), "weird") {
		t.Errorf("unmapped status must fail with the status name: %v", err)
	}
}
