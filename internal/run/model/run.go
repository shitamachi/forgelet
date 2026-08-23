package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// RunID uniquely identifies a WorkflowRun.
type RunID string

// JobRunID uniquely identifies a JobRun.
type JobRunID string

// RepositoryRef identifies a repository at a provider.
type RepositoryRef struct {
	Provider string // "github", "gitea", ...
	Owner    string
	Name     string
}

func (r RepositoryRef) String() string {
	return r.Provider + "/" + r.Owner + "/" + r.Name
}

// DeliveryKey is the unique idempotency key of a provider delivery.
type DeliveryKey struct {
	Provider   string
	DeliveryID string
}

func (k DeliveryKey) String() string {
	return k.Provider + ":" + k.DeliveryID
}

// Event is the minimal provider-neutral trigger information the durable run
// state needs. The full NormalizedEvent contract is owned by spec 0005.
type Event struct {
	Provider   string
	Name       string // "push", "pull_request", "workflow_dispatch", "schedule"
	DeliveryID string
	Repository RepositoryRef
	Ref        string // full ref, e.g. refs/heads/main
	SHA        string
	Actor      string
}

// Delivery is an incoming provider delivery to be durably recorded before any
// expensive processing. Payload is the immutable raw provider payload.
type Delivery struct {
	Key     DeliveryKey
	Event   Event
	Payload []byte
}

// DeliveryRecord is the durable receipt of a Delivery.
type DeliveryRecord struct {
	Key        DeliveryKey
	Event      Event
	Payload    []byte
	ReceivedAt time.Time
	// RunID is empty until a WorkflowRun has been created for this delivery.
	RunID RunID
}

// RunSeed describes the durable intent for one workflow run: the triggering
// delivery plus the compiled job instances.
type RunSeed struct {
	Delivery DeliveryKey
	Event    Event
	Jobs     []JobIntent
}

// JobIntent is one compiled job instance to persist as a JobRun.
type JobIntent struct {
	JobKey      string // workflow job id, e.g. "test" or "test[go=1.27,os=linux]"
	RunnerClass string
	DependsOn   []string          // job keys this instance waits for
	Matrix      map[string]string // matrix combination (stable, keys sorted at compile time)
	PlanDigest  string            // hex sha256 of the immutable Plan, may be empty in tests
}

// RunRecord is the durable record of a WorkflowRun.
type RunRecord struct {
	ID         RunID
	Event      Event
	Delivery   DeliveryKey
	Status     WorkflowRunStatus
	CreatedAt  time.Time
	FinishedAt *time.Time
}

// JobRunRecord is the durable record of a JobRun.
type JobRunRecord struct {
	ID           JobRunID
	RunID        RunID
	JobKey       string
	RunnerClass  string
	DependsOn    []string
	Matrix       map[string]string
	PlanDigest   string
	Status       JobRunStatus
	Attempt      int
	ActiveName   string // deterministic Kubernetes resource name, set on dispatch
	ActiveUID    string
	CreatedAt    time.Time
	DispatchedAt *time.Time
	StartedAt    *time.Time
	FinishedAt   *time.Time
	// ActiveCollectedAt records when the active object was confirmed
	// collected; a collected job is never re-scanned by the collector.
	ActiveCollectedAt *time.Time
}

// CRName returns the deterministic Kubernetes resource name for the JobRun.
func (id JobRunID) CRName() string {
	return "jobrun-" + strings.ToLower(string(id))
}

// ObservedPhase is the observed state of the active Kubernetes execution.
type ObservedPhase string

const (
	PhasePending   ObservedPhase = "pending"
	PhaseRunning   ObservedPhase = "running"
	PhaseSucceeded ObservedPhase = "succeeded"
	PhaseFailed    ObservedPhase = "failed"
)

// JobStatusFromPhase maps an observed phase to the durable JobRun status it
// implies. Pending implies no durable change.
func JobStatusFromPhase(p ObservedPhase) (JobRunStatus, bool) {
	switch p {
	case PhasePending:
		return "", false
	case PhaseRunning:
		return JobRunning, true
	case PhaseSucceeded:
		return JobSucceeded, true
	case PhaseFailed:
		return JobFailed, true
	default:
		return "", false
	}
}

// Digest returns the hex-encoded SHA-256 of b.
func Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Validate checks basic invariants of a job intent.
func (j JobIntent) Validate() error {
	if strings.TrimSpace(j.JobKey) == "" {
		return fmt.Errorf("job intent: empty job key")
	}
	if strings.TrimSpace(j.RunnerClass) == "" {
		return fmt.Errorf("job intent %q: empty runner class", j.JobKey)
	}
	return nil
}
