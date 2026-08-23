package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	forgeletv1 "github.com/shitamachi/forgelet/api/v1alpha1"
	"github.com/shitamachi/forgelet/internal/run/model"
)

func TestMapPodPhaseUnknownIsPending(t *testing.T) {
	cases := map[corev1.PodPhase]string{
		corev1.PodPending:   forgeletv1.JobRunPhasePending,
		corev1.PodRunning:   forgeletv1.JobRunPhaseRunning,
		corev1.PodSucceeded: forgeletv1.JobRunPhaseSucceeded,
		corev1.PodFailed:    forgeletv1.JobRunPhaseFailed,
		corev1.PodUnknown:   forgeletv1.JobRunPhasePending,
	}
	for in, want := range cases {
		if got := mapPodPhase(in); got != want {
			t.Errorf("mapPodPhase(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestToModelPhase(t *testing.T) {
	cases := map[string]model.ObservedPhase{
		forgeletv1.JobRunPhasePending:   model.PhasePending,
		forgeletv1.JobRunPhaseRunning:   model.PhaseRunning,
		forgeletv1.JobRunPhaseSucceeded: model.PhaseSucceeded,
		forgeletv1.JobRunPhaseFailed:    model.PhaseFailed,
		"garbage":                       model.PhasePending,
	}
	for in, want := range cases {
		if got := toModelPhase(in); got != want {
			t.Errorf("toModelPhase(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestJobRunTerminal(t *testing.T) {
	jr := &forgeletv1.JobRun{}
	if jr.Terminal() {
		t.Error("empty phase must not be terminal")
	}
	jr.Status.Phase = forgeletv1.JobRunPhaseSucceeded
	if !jr.Terminal() {
		t.Error("succeeded must be terminal")
	}
	jr.Status.Phase = forgeletv1.JobRunPhaseFailed
	if !jr.Terminal() {
		t.Error("failed must be terminal")
	}
	jr.Status.Phase = forgeletv1.JobRunPhaseRunning
	if jr.Terminal() {
		t.Error("running must not be terminal")
	}
}

func TestUpdateConditionsIdempotent(t *testing.T) {
	jr := testJobRun("jobrun-x")
	r, c, _ := newReconciler(t, jr, testRunnerClass())
	ctx := context.Background()

	// First call writes the condition (the fake client persists the object
	// with status subresource).
	if err := r.updateConditions(ctx, jr, metav1.ConditionFalse, forgeletv1.ReasonRunnerClassMissing, " RunnerClass \"k3s-small\" not found"); err != nil {
		t.Fatalf("updateConditions: %v", err)
	}
	var got forgeletv1.JobRun
	if err := c.Get(ctx, types.NamespacedName{Namespace: testNS, Name: jr.Name}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Status.Conditions) != 1 {
		t.Fatalf("conditions = %d, want 1", len(got.Status.Conditions))
	}
	// Same input again: no-op success.
	if err := r.updateConditions(ctx, &got, metav1.ConditionFalse, forgeletv1.ReasonRunnerClassMissing, " RunnerClass \"k3s-small\" not found"); err != nil {
		t.Fatalf("idempotent updateConditions: %v", err)
	}
}
