package controller

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	forgeletv1 "github.com/shitamachi/forgelet/api/v1alpha1"
)

// NewScheme builds the scheme with core Kubernetes and forgelet CRD types;
// both the controller and the server's Kubernetes active store need it.
func NewScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		panic(err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		panic(err)
	}
	if err := forgeletv1.AddToScheme(s); err != nil {
		panic(err)
	}
	return s
}
