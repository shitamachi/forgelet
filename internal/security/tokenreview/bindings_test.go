package tokenreview

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/shitamachi/forgelet/internal/run/model"
)

func pod(name, uid string, labels map[string]string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: "forgelet-jobs", Name: name, UID: types.UID(uid), Labels: labels,
	}}
}

func TestPodLabelBindingsResolves(t *testing.T) {
	client := fake.NewSimpleClientset(
		pod("job-1-pod", "uid-1", map[string]string{DefaultJobRunLabel: "01HJOB"}),
	)
	b := NewPodLabelBindings(client)
	id, err := b.JobRunForPod(context.Background(), "forgelet-jobs", "job-1-pod", "uid-1")
	if err != nil || id != model.JobRunID("01HJOB") {
		t.Fatalf("binding = %q err=%v", id, err)
	}
}

func TestPodLabelBindingsRejections(t *testing.T) {
	client := fake.NewSimpleClientset(
		pod("job-1-pod", "uid-1", nil),
		pod("job-2-pod", "uid-2", map[string]string{"other/label": "x"}),
	)
	b := NewPodLabelBindings(client)

	if _, err := b.JobRunForPod(context.Background(), "forgelet-jobs", "job-1-pod", "WRONG-UID"); err == nil ||
		!strings.Contains(err.Error(), "does not match") {
		t.Errorf("uid mismatch: %v", err)
	}
	if _, err := b.JobRunForPod(context.Background(), "forgelet-jobs", "missing", "u"); err == nil {
		t.Error("unknown pod must fail")
	}
	if _, err := b.JobRunForPod(context.Background(), "forgelet-jobs", "job-1-pod", "uid-1"); err == nil ||
		!strings.Contains(err.Error(), DefaultJobRunLabel) {
		t.Errorf("pod without label: %v", err)
	}
}
