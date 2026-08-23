package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Observed phases of a JobRun, mirroring the Kubernetes execution state.
// Durable business state lives in PostgreSQL (spec 0002); this is the
// observed side only.
const (
	JobRunPhasePending   = "pending"
	JobRunPhaseRunning   = "running"
	JobRunPhaseSucceeded = "succeeded"
	JobRunPhaseFailed    = "failed"
)

// Conditions set by the controller.
const (
	JobRunConditionReady = "Ready"

	ReasonRunnerClassMissing = "RunnerClassMissing"
	ReasonProgressing        = "Progressing"
)

// JobRunSpec is the desired active execution of one compiled job instance.
type JobRunSpec struct {
	// RunID is the forgelet WorkflowRun ID this job belongs to.
	RunID string `json:"runId"`
	// JobKey identifies the job in the workflow, e.g. "test[go=1.27]".
	JobKey string `json:"jobKey"`
	// RunnerClass names the RunnerClass providing the execution profile.
	RunnerClass string `json:"runnerClass"`
	// PlanID references the immutable plan in the control plane.
	PlanID string `json:"planId"`
	// PlanDigest is the hex sha256 of the plan; the executor verifies it.
	PlanDigest string `json:"planDigest"`
	// Attempt is the execution attempt number, starting at 1.
	Attempt int32 `json:"attempt"`
}

// JobRunStatus is the observed state of the active execution.
type JobRunStatus struct {
	// Phase is the observed execution phase (pending/running/succeeded/failed).
	// +optional
	Phase string `json:"phase,omitempty"`
	// PodName/PodUID identify the primary Pod. Owned by the controller.
	// +optional
	PodName string `json:"podName,omitempty"`
	// +optional
	PodUID string `json:"podUid,omitempty"`
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=jrun
//+kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
//+kubebuilder:printcolumn:name="Pod",type=string,JSONPath=`.status.podName`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// JobRun is the active Kubernetes execution of one compiled job instance.
type JobRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   JobRunSpec   `json:"spec,omitempty"`
	Status JobRunStatus `json:"status,omitempty"`
}

// Terminal reports whether the observed phase is final.
func (r *JobRun) Terminal() bool {
	return r.Status.Phase == JobRunPhaseSucceeded || r.Status.Phase == JobRunPhaseFailed
}

//+kubebuilder:object:root=true

// JobRunList contains a list of JobRun.
type JobRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []JobRun `json:"items"`
}
