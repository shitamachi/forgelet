package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Workspace modes. M0 always uses emptyDir; pvc exists for the P2
// Docker Action workspace (spec 0001 FR-4.5).
const (
	WorkspaceModeAuto     = "auto"
	WorkspaceModeEmptyDir = "emptyDir"
	WorkspaceModePVC      = "pvc"
)

// WorkspaceSpec configures the job workspace volume.
type WorkspaceSpec struct {
	// Mode is auto, emptyDir or pvc. +kubebuilder:default=auto
	// +kubebuilder:validation:Enum=auto;emptyDir;pvc
	// +optional
	Mode string `json:"mode,omitempty"`
	// StorageClassName applies to pvc mode only.
	// +optional
	StorageClassName string `json:"storageClassName,omitempty"`
	// Size is the pvc size, e.g. 10Gi.
	// +optional
	Size string `json:"size,omitempty"`
}

// SecuritySpec constrains what jobs on this class may receive.
type SecuritySpec struct {
	// AllowSecrets gates secret delivery to jobs on this class; untrusted
	// classes (fork PRs) set it to false (spec 0001 FR-9.4).
	// +optional
	AllowSecrets *bool `json:"allowSecrets,omitempty"`
	// Labels select nodes reserved for this trust level, e.g. ci.security=sandbox.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
}

// RunnerClassSpec is the execution profile referenced by `runs-on`.
type RunnerClassSpec struct {
	// Image is the default job container image.
	Image string `json:"image"`
	// Resources are applied to the job container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
	// NodeSelector constrains scheduling.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// Workspace configures the workspace volume.
	// +optional
	Workspace WorkspaceSpec `json:"workspace,omitempty"`
	// Security is the trust profile of this class.
	// +optional
	Security SecuritySpec `json:"security,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:shortName=rclass
//+kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.image`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// RunnerClass is the infrastructure profile a job's `runs-on` resolves to.
// It describes resources, not a runner machine (spec 0001 FR-4.3).
type RunnerClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec RunnerClassSpec `json:"spec,omitempty"`
}

//+kubebuilder:object:root=true

// RunnerClassList contains a list of RunnerClass.
type RunnerClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RunnerClass `json:"items"`
}
