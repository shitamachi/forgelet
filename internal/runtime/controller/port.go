// Package controller reconciles JobRun CRs into primary Pods and projects
// observed execution state back to the durable store through a port. It is
// the only component with Kubernetes API permissions (spec 0001 FR-4.2/4.4).
package controller

import (
	"context"
	"time"

	"github.com/shitamachi/forgelet/internal/run/model"
)

// DurableProjection is the consumer-side port through which the controller
// reports observed Kubernetes state. The server wires it to the durable
// store (0002 ApplyObserved semantics); the controller never imports a
// storage adapter (module boundaries).
type DurableProjection interface {
	ApplyObserved(ctx context.Context, id model.JobRunID, phase model.ObservedPhase, now time.Time) error
}

// JobRunSource supplies durable JobRun data needed to fill a JobRun CR spec.
type JobRunSource interface {
	Get(ctx context.Context, id model.JobRunID) (model.JobRunRecord, error)
}
