// Package v1alpha1 contains the forgelet CRD API types: RunnerClass,
// WorkflowRun and JobRun. CRDs never carry secrets or execution plans —
// JobRun references a plan by ID and digest only (spec 0001 FR-9.2).
//
// +kubebuilder:object:generate=true
// +groupName=ci.forgelet.dev
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is the API group and version of the forgelet CRDs.
	GroupVersion = schema.GroupVersion{Group: "ci.forgelet.dev", Version: "v1alpha1"}

	// schemeBuilder registers the CRD types with any runtime scheme.
	schemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds all forgelet types to a scheme.
	AddToScheme = schemeBuilder.AddToScheme
)

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion,
		&JobRun{}, &JobRunList{},
		&WorkflowRun{}, &WorkflowRunList{},
		&RunnerClass{}, &RunnerClassList{},
	)
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}

//go:generate go run sigs.k8s.io/controller-tools/cmd/controller-gen object paths=./...
//go:generate go run sigs.k8s.io/controller-tools/cmd/controller-gen crd paths=./... output:crd:artifacts:config=../../deploy/crds
