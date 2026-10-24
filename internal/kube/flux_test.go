package kube

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/shitamachi/forgelet/internal/delivery"
)

func TestFluxRequiresCurrentSourceInventoryAndApplicationHealth(t *testing.T) {
	target := delivery.Target{Repository: "o/gitops", Branch: "main", FluxNamespace: "flux-system", Kustomization: "app", Namespace: "apps", Deployment: "app", Container: "app"}
	revision := "main@sha1:" + strings.Repeat("a", 40)
	ready := []any{map[string]any{"type": "Ready", "status": "True"}}
	k := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"name": "app", "namespace": "flux-system", "generation": int64(2)},
		"spec":     map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "source"}},
		"status": map[string]any{"observedGeneration": int64(2), "conditions": ready, "lastAppliedRevision": revision,
			"inventory": map[string]any{"entries": []any{map[string]any{"id": "apps_app_apps_Deployment", "v": "v1"}}}},
	}}
	source := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "GitRepository",
		"metadata": map[string]any{"name": "source", "namespace": "flux-system", "generation": int64(1)},
		"spec":     map[string]any{"url": target.SourceURL(), "ref": map[string]any{"branch": "main"}},
		"status":   map[string]any{"observedGeneration": int64(1), "conditions": ready, "artifact": map[string]any{"revision": revision}},
	}}
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "apps", Generation: 3}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "ghcr.io/o/app@sha256:" + strings.Repeat("b", 64)}}}}}, Status: appsv1.DeploymentStatus{ObservedGeneration: 3, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1}}
	set := func(o *unstructured.Unstructured, value any, path ...string) {
		if err := unstructured.SetNestedField(o.Object, value, path...); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name       string
		change     func(*unstructured.Unstructured, *unstructured.Unstructured, *appsv1.Deployment)
		ready, err bool
	}{
		{name: "current and healthy", ready: true},
		{name: "old Flux generation", change: func(k, _ *unstructured.Unstructured, _ *appsv1.Deployment) {
			set(k, int64(1), "status", "observedGeneration")
		}},
		{name: "suspended", change: func(k, _ *unstructured.Unstructured, _ *appsv1.Deployment) { set(k, true, "spec", "suspend") }},
		{name: "reconciling", change: func(k, _ *unstructured.Unstructured, _ *appsv1.Deployment) {
			set(k, append(ready, map[string]any{"type": "Reconciling", "status": "True"}), "status", "conditions")
		}},
		{name: "source revision differs", change: func(_, s *unstructured.Unstructured, _ *appsv1.Deployment) {
			set(s, "main@sha1:"+strings.Repeat("c", 40), "status", "artifact", "revision")
		}},
		{name: "wrong repository", err: true, change: func(_, s *unstructured.Unstructured, _ *appsv1.Deployment) {
			set(s, "https://github.com/other/repo.git", "spec", "url")
		}},
		{name: "wrong branch", err: true, change: func(_, s *unstructured.Unstructured, _ *appsv1.Deployment) { set(s, "other", "spec", "ref", "branch") }},
		{name: "unrelated deployment", change: func(k, _ *unstructured.Unstructured, _ *appsv1.Deployment) {
			unstructured.RemoveNestedField(k.Object, "status", "inventory")
		}},
		{name: "old deployment generation", change: func(_, _ *unstructured.Unstructured, d *appsv1.Deployment) { d.Status.ObservedGeneration-- }},
		{name: "old replica still serving", change: func(_, _ *unstructured.Unstructured, d *appsv1.Deployment) { d.Status.UpdatedReplicas = 0 }},
		{name: "not ready", change: func(_, _ *unstructured.Unstructured, d *appsv1.Deployment) { d.Status.ReadyReplicas = 0 }},
		{name: "missing container", err: true, change: func(_, _ *unstructured.Unstructured, d *appsv1.Deployment) {
			d.Spec.Template.Spec.Containers[0].Name = "other"
		}},
		{name: "scaled to zero", change: func(_, _ *unstructured.Unstructured, d *appsv1.Deployment) { n := int32(0); d.Spec.Replicas = &n }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k, source, d := k.DeepCopy(), source.DeepCopy(), d.DeepCopy()
			if tc.change != nil {
				tc.change(k, source, d)
			}
			observer := FluxObserver{Dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), k, source), Client: fake.NewClientset(d)}
			got, err := observer.Observe(t.Context(), target)
			if (err != nil) != tc.err || got.Ready != tc.ready || got.Revision != revision {
				t.Fatalf("observation=%+v err=%v", got, err)
			}
		})
	}
}
