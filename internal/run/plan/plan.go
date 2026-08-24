// Package plan defines the immutable executor Plan and its stable digest.
// A persisted Plan contains secret references only, never resolved values.
package plan

import (
	"encoding/json"
	"fmt"

	"github.com/shitamachi/forgelet/internal/run/model"
)

// SecretRef references a secret by scope and name. It never carries a value.
type SecretRef struct {
	Scope string `json:"scope"` // "organization" | "repository" | "environment"
	Name  string `json:"name"`
	// Env is the environment variable the executor will expose the resolved
	// value under, when the reference is used as job-level secret injection.
	Env string `json:"env,omitempty"`
}

// RunStep is a shell step with optional per-step environment.
type RunStep struct {
	Script string            `json:"script"`
	Shell  string            `json:"shell,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
}

// BuiltinStep is a compiled `uses:` reference executed by an in-process
// handler in the primary container (spec 0009). Exactly one of Run/Builtin
// is set on any Step.
type BuiltinStep struct {
	Action  string            `json:"action"`
	Version string            `json:"version,omitempty"`
	Inputs  map[string]string `json:"inputs,omitempty"`
}

// Step is one executable step of the Plan.
type Step struct {
	ID      string       `json:"id"`
	Name    string       `json:"name,omitempty"`
	If      string       `json:"if,omitempty"` // raw condition; evaluated by the executor
	Run     RunStep      `json:"run"`
	Builtin *BuiltinStep `json:"builtin,omitempty"`
	// ContinueOnError keeps a failing step from failing the job (outcome
	// failure, conclusion success).
	ContinueOnError bool `json:"continueOnError,omitempty"`
	// WorkingDir is relative to the workspace root.
	WorkingDir string `json:"workingDir,omitempty"`
}

// Plan is everything the executor needs to run one JobRun. It is immutable
// after creation; treat it as a value.
type Plan struct {
	JobRunID    model.JobRunID      `json:"jobRunId"`
	Repository  model.RepositoryRef `json:"repository"`
	EventName   string              `json:"eventName,omitempty"` // github.event_name
	Actor       string              `json:"actor,omitempty"`     // github.actor
	SHA         string              `json:"sha"`
	Ref         string              `json:"ref"`
	RunnerClass string              `json:"runnerClass"`
	Env         map[string]string   `json:"env,omitempty"`
	Steps       []Step              `json:"steps"`
	SecretRefs  []SecretRef         `json:"secretRefs,omitempty"`
	TimeoutSecs int                 `json:"timeoutSecs,omitempty"`
}

// Digest returns the hex SHA-256 of the canonical JSON encoding of the Plan.
// The encoding is stable: struct field order is fixed and encoding/json sorts
// map keys, so equal plans built with different map insertion orders digest
// identically. Secret references are included; secret values never are,
// because a Plan cannot carry them.
func (p *Plan) Digest() (string, error) {
	if p == nil {
		return "", fmt.Errorf("plan: digest of nil plan")
	}
	if p.JobRunID == "" {
		return "", fmt.Errorf("plan: empty job run id")
	}
	if len(p.Steps) == 0 {
		return "", fmt.Errorf("plan %s: no steps", p.JobRunID)
	}
	b, err := p.canonicalJSON()
	if err != nil {
		return "", fmt.Errorf("plan %s: canonical encode: %w", p.JobRunID, err)
	}
	return model.Digest(b), nil
}

func (p *Plan) canonicalJSON() ([]byte, error) {
	return json.Marshal(p)
}
