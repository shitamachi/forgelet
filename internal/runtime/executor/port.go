// Package executor runs one forgelet job: sequential run steps sharing a
// workspace, GitHub file commands, secret masking before any log output,
// process-group cancellation, and status reporting to the control plane.
// It never touches Kubernetes, providers or databases directly.
package executor

import (
	"context"

	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/run/plan"
	"github.com/shitamachi/forgelet/internal/security/identity"
)

// StepResult is the outcome of one step.
type StepResult struct {
	StepID     string `json:"stepId"`
	ExitCode   int    `json:"exitCode"`
	DurationMs int64  `json:"durationMs"`
	// Outcome is what happened (success/failure/skipped); conclusion folds
	// in continue-on-error (a continued failure concludes success).
	Outcome    string `json:"outcome,omitempty"`
	Conclusion string `json:"conclusion,omitempty"`
}

// JobResult is the final report of a job run.
type JobResult struct {
	JobRunID  model.JobRunID `json:"jobRunId"`
	Success   bool           `json:"success"`
	Cancelled bool           `json:"cancelled,omitempty"`
	Steps     []StepResult   `json:"steps"`
	Error     string         `json:"error,omitempty"`
}

// ControlPlane is the executor-side contract to the forgelet control plane
// (spec 0008 FR-X4). All calls carry the JobRun-scoped workload identity.
type ControlPlane interface {
	// FetchPlan returns the immutable plan for the identity's JobRun.
	FetchPlan(ctx context.Context, id identity.Identity) (plan.Plan, error)

	// FetchSecrets resolves exactly the requested references; the response
	// contains only authorized values and must never be logged.
	FetchSecrets(ctx context.Context, id identity.Identity, refs []plan.SecretRef) (map[string]string, error)

	// ReportJob reports the terminal result; idempotent on the server side.
	ReportJob(ctx context.Context, id identity.Identity, result JobResult) error

	// ResolveCache attempts to restore a cache entry. hit indicates whether
	// getURL is usable; putURL is always a presigned PUT for the exact key.
	ResolveCache(ctx context.Context, id identity.Identity, key string, restoreKeys []string) (hit bool, getURL string, putURL string, err error)

	// ArtifactUploadURL returns a presigned PUT URL for uploading an artifact.
	ArtifactUploadURL(ctx context.Context, id identity.Identity, name string) (string, error)

	// ArtifactDownloadURL returns a presigned GET URL for downloading an artifact.
	ArtifactDownloadURL(ctx context.Context, id identity.Identity, name string) (string, error)
}
