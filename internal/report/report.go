// Package report defines the check-reporting port: how forgelet job state
// is surfaced on a provider (GitHub Check Runs). The mapping from durable
// job status to check status/conclusion is a pure function; adapters live
// in the provider packages.
package report

import (
	"context"
	"fmt"
	"time"

	"github.com/shitamachi/forgelet/internal/run/model"
)

// CheckStatus follows the GitHub Check Run lifecycle.
type CheckStatus string

const (
	StatusQueued    CheckStatus = "queued"
	StatusProgress  CheckStatus = "in_progress"
	StatusCompleted CheckStatus = "completed"
)

// CheckConclusion is set when status is completed.
type CheckConclusion string

const (
	ConclusionSuccess   CheckConclusion = "success"
	ConclusionFailure   CheckConclusion = "failure"
	ConclusionCancelled CheckConclusion = "cancelled"
)

// Check is one reportable state of a forgelet job.
type Check struct {
	RunID       model.RunID
	JobRunID    model.JobRunID
	Name        string // job key, e.g. "test"
	ExternalID  string // forgelet JobRun ID — upsert key on the provider
	DetailsURL  string
	Status      CheckStatus
	Conclusion  CheckConclusion
	StartedAt   *time.Time
	CompletedAt *time.Time
}

// CheckReporter reports job state to a provider. Implementations must be
// idempotent per (ExternalID, Status) and must never carry secrets or
// credentials in errors.
type CheckReporter interface {
	// Report upserts the check for the given run and job. The run carries
	// repo/SHA context the adapter needs (provider-neutral).
	Report(ctx context.Context, run model.RunRecord, check Check) error
}

// MapJobRun builds the Check for a durable job record. detailsBase is the
// forgelet UI base URL; the job page path is /runs/{run}/jobs/{jobKey}.
func MapJobRun(job model.JobRunRecord, detailsBase string) (Check, error) {
	if job.JobKey == "" {
		return Check{}, fmt.Errorf("report: job %s has empty job key", job.ID)
	}
	c := Check{
		RunID:      job.RunID,
		JobRunID:   job.ID,
		Name:       job.JobKey,
		ExternalID: string(job.ID),
		DetailsURL: fmt.Sprintf("%s/runs/%s/jobs/%s", detailsBase, job.RunID, job.JobKey),
		StartedAt:  job.StartedAt,
	}
	switch job.Status {
	case model.JobQueued:
		c.Status = StatusQueued
	case model.JobDispatched, model.JobRunning:
		c.Status = StatusProgress
	case model.JobSucceeded:
		c.Status, c.Conclusion = StatusCompleted, ConclusionSuccess
		c.CompletedAt = job.FinishedAt
	case model.JobFailed:
		c.Status, c.Conclusion = StatusCompleted, ConclusionFailure
		c.CompletedAt = job.FinishedAt
	case model.JobCancelled:
		c.Status, c.Conclusion = StatusCompleted, ConclusionCancelled
		c.CompletedAt = job.FinishedAt
	default:
		return Check{}, fmt.Errorf("report: unmapped job status %q", job.Status)
	}
	return c, nil
}
