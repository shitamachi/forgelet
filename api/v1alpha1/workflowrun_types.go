package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WorkflowRunPhases observed on the cluster side. Durable run state and
// history live in PostgreSQL (spec 0002).
const (
	WorkflowRunPhaseActive   = "active"
	WorkflowRunPhaseTerminal = "terminal"
)

// RepositoryRef identifies a repository at a provider.
type RepositoryRef struct {
	Provider string `json:"provider"`
	Owner    string `json:"owner"`
	Name     string `json:"name"`
}

// WorkflowRunSpec is the in-cluster reference to a durable workflow run.
// It deliberately carries no history: only the fields needed to correlate
// JobRuns and reports with the control plane.
type WorkflowRunSpec struct {
	// RunID is the forgelet WorkflowRun ID (durable key).
	RunID string `json:"runId"`
	// DeliveryKey is "provider:deliveryID" of the triggering delivery.
	DeliveryKey string `json:"deliveryKey"`
	// Event is the normalized trigger name (push, pull_request, ...).
	Event string `json:"event"`
	// Repository is the source repository.
	Repository RepositoryRef `json:"repository"`
	// Ref is the full ref, e.g. refs/heads/main.
	Ref string `json:"ref"`
	// SHA is the commit being built.
	SHA string `json:"sha"`
}

// WorkflowRunStatus is the observed aggregate of the run's JobRuns.
type WorkflowRunStatus struct {
	// Phase is active while any JobRun is non-terminal, terminal otherwise.
	// +optional
	Phase string `json:"phase,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Run",type=string,JSONPath=`.spec.runId`
//+kubebuilder:printcolumn:name="Event",type=string,JSONPath=`.spec.event`
//+kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// WorkflowRun is the in-cluster reference to a workflow run.
type WorkflowRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkflowRunSpec   `json:"spec,omitempty"`
	Status WorkflowRunStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// WorkflowRunList contains a list of WorkflowRun.
type WorkflowRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkflowRun `json:"items"`
}
