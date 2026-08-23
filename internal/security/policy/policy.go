// Package policy holds the pure authorization decisions of the security
// module: execution binding checks and secret delivery. Decisions reference
// secrets by scope and name only; values never pass through this package.
package policy

import (
	"errors"
	"fmt"

	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/security/identity"
)

// TrustLevel classifies the source of a run.
type TrustLevel string

const (
	// TrustTrusted: push from a protected branch or manual dispatch.
	TrustTrusted TrustLevel = "trusted"
	// TrustSameRepo: PR from a branch in the same repository.
	TrustSameRepo TrustLevel = "same-repo"
	// TrustFork: PR from a fork — deny all secrets (spec 0001 FR-9.4).
	TrustFork TrustLevel = "fork"
)

// Ref identifies a secret by scope and name.
type Ref struct {
	Scope string // "organization" | "repository" | "environment"
	Name  string
}

// ErrNotAuthorized marks authorization failures.
var ErrNotAuthorized = errors.New("policy: not authorized")

// AuthorizeExecution verifies that the identity may act on the given
// JobRun: JobRun binding must match exactly.
func AuthorizeExecution(id identity.Identity, jobRun model.JobRunID) error {
	if id.JobRunID != jobRun {
		return fmt.Errorf("%w: identity bound to job run %s, requested %s", ErrNotAuthorized, id.JobRunID, jobRun)
	}
	return nil
}

// AuthorizeObservation verifies that the identity may project an observed
// phase for the given JobRun. Executor identities stay bound to their own
// JobRun; identities holding observed:write (the controller) may observe
// every JobRun — that scope exists only on control-plane tokens.
func AuthorizeObservation(id identity.Identity, jobRun model.JobRunID) error {
	if id.HasScope(identity.ScopeObservedWrite) {
		return nil
	}
	if id.JobRunID == jobRun {
		return nil
	}
	return fmt.Errorf("%w: identity bound to job run %s, requested %s", ErrNotAuthorized, id.JobRunID, jobRun)
}

// DeniedRef is a requested secret that was refused, with a reason that never
// contains the secret value.
type DeniedRef struct {
	Ref    Ref
	Reason string
}

// Decision is the outcome of a secret delivery request.
type Decision struct {
	Allowed []Ref
	Denied  []DeniedRef
}

// DecideSecrets returns which of the requested secrets the identity may
// receive: the identity needs secrets:read, a fork-trust run gets nothing,
// and only secrets referenced by the job's plan are ever allowed.
func DecideSecrets(id identity.Identity, requested, planRefs []Ref, trust TrustLevel) Decision {
	var d Decision
	deny := func(r Ref, reason string) {
		d.Denied = append(d.Denied, DeniedRef{Ref: r, Reason: reason})
	}

	if !id.HasScope(identity.ScopeSecretsRead) {
		for _, r := range requested {
			deny(r, "identity lacks scope "+identity.ScopeSecretsRead)
		}
		return d
	}
	if trust == TrustFork {
		for _, r := range requested {
			deny(r, "fork-pull-request runs receive no secrets")
		}
		return d
	}

	allowed := make(map[Ref]bool, len(planRefs))
	for _, r := range planRefs {
		allowed[r] = true
	}
	for _, r := range requested {
		if allowed[r] {
			d.Allowed = append(d.Allowed, r)
		} else {
			deny(r, "not referenced by the job plan")
		}
	}
	return d
}
