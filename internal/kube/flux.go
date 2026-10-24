package kube

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/shitamachi/forgelet/internal/delivery"
)

type FluxObserver struct {
	Dynamic dynamic.Interface
	Client  kubernetes.Interface
}

var kustomizations = schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
var gitRepositories = schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}

func (f *FluxObserver) Observe(ctx context.Context, t delivery.Target) (delivery.Observation, error) {
	var result delivery.Observation
	k, err := f.Dynamic.Resource(kustomizations).Namespace(t.FluxNamespace).Get(ctx, t.Kustomization, metav1.GetOptions{})
	if err != nil {
		return result, fmt.Errorf("observe Flux Kustomization: %w", err)
	}
	result.Revision, _, _ = unstructured.NestedString(k.Object, "status", "lastAppliedRevision")
	sourceKind, _, _ := unstructured.NestedString(k.Object, "spec", "sourceRef", "kind")
	sourceName, _, _ := unstructured.NestedString(k.Object, "spec", "sourceRef", "name")
	sourceNS, _, _ := unstructured.NestedString(k.Object, "spec", "sourceRef", "namespace")
	if sourceNS == "" {
		sourceNS = t.FluxNamespace
	}
	if sourceKind != "GitRepository" || sourceName == "" {
		return result, fmt.Errorf("flux source must be a GitRepository")
	}
	source, err := f.Dynamic.Resource(gitRepositories).Namespace(sourceNS).Get(ctx, sourceName, metav1.GetOptions{})
	if err != nil {
		return result, err
	}
	sourceURL, _, _ := unstructured.NestedString(source.Object, "spec", "url")
	branch, _, _ := unstructured.NestedString(source.Object, "spec", "ref", "branch")
	if strings.TrimSuffix(sourceURL, ".git") != strings.TrimSuffix(t.SourceURL(), ".git") || branch != t.Branch {
		return result, fmt.Errorf("flux source differs from registered GitOps repository/branch")
	}
	artifact, _, _ := unstructured.NestedString(source.Object, "status", "artifact", "revision")
	if artifact != result.Revision || !fluxReady(k) || !fluxReady(source) {
		return result, nil
	}
	d, err := f.Client.AppsV1().Deployments(t.Namespace).Get(ctx, t.Deployment, metav1.GetOptions{})
	if err != nil {
		return result, err
	}
	for _, container := range d.Spec.Template.Spec.Containers {
		if container.Name == t.Container {
			result.Image = container.Image
		}
	}
	if result.Image == "" {
		return result, fmt.Errorf("registered application container is missing")
	}
	replicas := int32(1)
	if d.Spec.Replicas != nil {
		replicas = *d.Spec.Replicas
	}
	if replicas <= 0 || d.Status.ObservedGeneration < d.Generation || d.Status.UpdatedReplicas != replicas || d.Status.Replicas != replicas || d.Status.AvailableReplicas != replicas || d.Status.ReadyReplicas != replicas || d.Status.UnavailableReplicas != 0 {
		return result, nil
	}
	// Ensure Flux actually inventories this Deployment, rather than accepting
	// unrelated healthy workloads with the same image.
	entries, _, _ := unstructured.NestedSlice(k.Object, "status", "inventory", "entries")
	for _, entry := range entries {
		if m, ok := entry.(map[string]any); ok && m["id"] == t.Namespace+"_"+t.Deployment+"_apps_Deployment" {
			result.Ready = true
			break
		}
	}
	return result, nil
}
func fluxReady(object *unstructured.Unstructured) bool {
	if suspended, _, _ := unstructured.NestedBool(object.Object, "spec", "suspend"); suspended {
		return false
	}
	observed, _, _ := unstructured.NestedInt64(object.Object, "status", "observedGeneration")
	if observed != object.GetGeneration() {
		return false
	}
	conditions, _, _ := unstructured.NestedSlice(object.Object, "status", "conditions")
	ready := false
	for _, raw := range conditions {
		c, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if c["type"] == "Ready" && c["status"] == "True" {
			ready = true
		}
		if (c["type"] == "Reconciling" || c["type"] == "Stalled") && c["status"] == "True" {
			return false
		}
	}
	return ready
}
