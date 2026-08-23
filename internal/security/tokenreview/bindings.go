package tokenreview

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/shitamachi/forgelet/internal/run/model"
)

// DefaultJobRunLabel is the pod label carrying the owning JobRun id
// (0004 §4 Pod template).
const DefaultJobRunLabel = "ci.forgelet.dev/jobrun-id"

// PodLabelBindings resolves the JobRun binding from the executor pod's
// label, refusing pods whose UID does not match the reviewed one. It needs
// `get pods` RBAC for the control plane in the job namespace (0011 T8).
type PodLabelBindings struct {
	Client kubernetes.Interface
	Label  string
}

// NewPodLabelBindings wires PodLabelBindings with DefaultJobRunLabel.
func NewPodLabelBindings(client kubernetes.Interface) *PodLabelBindings {
	return &PodLabelBindings{Client: client, Label: DefaultJobRunLabel}
}

// JobRunForPod implements BindingSource.
func (p *PodLabelBindings) JobRunForPod(ctx context.Context, namespace, podName, podUID string) (model.JobRunID, error) {
	pod, err := p.Client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("load pod %s/%s: %w", namespace, podName, err)
	}
	if string(pod.UID) != podUID {
		return "", fmt.Errorf("pod %s/%s uid %q does not match the reviewed %q", namespace, podName, pod.UID, podUID)
	}
	id := pod.Labels[p.Label]
	if id == "" {
		return "", fmt.Errorf("pod %s/%s has no %q label", namespace, podName, p.Label)
	}
	return model.JobRunID(id), nil
}
