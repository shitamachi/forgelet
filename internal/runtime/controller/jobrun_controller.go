package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	forgeletv1 "github.com/shitamachi/forgelet/api/v1alpha1"
	"github.com/shitamachi/forgelet/internal/run/model"
)

// Well-known names on the job pods.
const (
	// ExecutorServiceAccount carries no Kubernetes RBAC; its only use is the
	// audience-bound projected token (spec 0001 FR-9.1).
	ExecutorServiceAccount = "forgelet-executor"

	// ControlPlaneAudience matches internal/security/identity.Audience.
	ControlPlaneAudience = "forgelet-control-plane"

	// PodSuffix builds the deterministic primary pod name: <jobrun>-pod.
	PodSuffix = "-pod"

	workspaceVolume = "workspace"
	tokenVolume     = "control-plane-token"
	tokenMountDir   = "/var/run/forgelet"
	workspaceDir    = "/workspace"
	executorPath    = "/ci/executor"

	jobContainer = "job"
	appLabel     = "ci.forgelet.dev/app"
	jobRunLabel  = "ci.forgelet.dev/jobrun-id"
)

// JobRunReconciler ensures every non-terminal JobRun CR has exactly one
// primary Pod and projects the observed phase to the durable store.
type JobRunReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	Projection DurableProjection
	Now        func() time.Time
}

// +kubebuilder:rbac:groups=ci.forgelet.dev,resources=jobruns,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=ci.forgelet.dev,resources=jobruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ci.forgelet.dev,resources=runnerclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;delete

// Reconcile drives one JobRun towards its desired state.
func (r *JobRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var jr forgeletv1.JobRun
	if err := r.Get(ctx, req.NamespacedName, &jr); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get jobrun %s: %w", req.Name, err)
	}

	// Enforce the deterministic naming protocol on every reconcile.
	if _, err := IDFromCRName(jr.Name); err != nil {
		return ctrl.Result{}, err
	}

	var class forgeletv1.RunnerClass
	if err := r.Get(ctx, types.NamespacedName{Namespace: jr.Namespace, Name: jr.Spec.RunnerClass}, &class); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("get runnerclass %s: %w", jr.Spec.RunnerClass, err)
		}
		// Missing class: surface it and retry; never create a pod blindly.
		if err := r.updateConditions(ctx, &jr, metav1.ConditionFalse, forgeletv1.ReasonRunnerClassMissing,
			fmt.Sprintf("RunnerClass %q not found", jr.Spec.RunnerClass)); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	podName := jr.Name + PodSuffix
	var pod corev1.Pod
	err := r.Get(ctx, types.NamespacedName{Namespace: jr.Namespace, Name: podName}, &pod)

	if apierrors.IsNotFound(err) {
		if jr.Terminal() {
			// Terminal and already collected: nothing to do, never recreate.
			return ctrl.Result{}, nil
		}
		if err := r.updateConditions(ctx, &jr, metav1.ConditionTrue, forgeletv1.ReasonProgressing, "creating primary pod"); err != nil {
			return ctrl.Result{}, err
		}
		newPod := buildPod(&jr, &class)
		if err := ctrl.SetControllerReference(&jr, newPod, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("set owner ref for pod %s: %w", podName, err)
		}
		if err := r.Create(ctx, newPod); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return ctrl.Result{RequeueAfter: time.Second}, nil
			}
			return ctrl.Result{}, fmt.Errorf("create pod %s: %w", podName, err)
		}
		logger.Info("created primary pod", "pod", podName, "jobrun", jr.Name)
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("get pod %s: %w", podName, err)
	}

	return r.observePod(ctx, &jr, &pod)
}

// observePod maps the pod phase onto the CR status and, on change, projects
// it to the durable store exactly once per phase transition.
func (r *JobRunReconciler) observePod(ctx context.Context, jr *forgeletv1.JobRun, pod *corev1.Pod) (ctrl.Result, error) {
	phase := mapPodPhase(pod.Status.Phase)
	now := r.Now()

	// Stale observations never regress the CR status (the durable store is
	// monotonic too; this keeps both sides consistent).
	if phaseRank(phase) < phaseRank(jr.Status.Phase) {
		return ctrl.Result{}, nil
	}
	changed := jr.Status.Phase != phase ||
		jr.Status.PodName != pod.Name ||
		jr.Status.PodUID != string(pod.UID)

	if !changed {
		return ctrl.Result{}, nil
	}

	jr.Status.Phase = phase
	jr.Status.PodName = pod.Name
	jr.Status.PodUID = string(pod.UID)
	switch phase {
	case forgeletv1.JobRunPhaseRunning:
		if jr.Status.StartedAt == nil {
			t := metav1.NewTime(now)
			jr.Status.StartedAt = &t
		}
	case forgeletv1.JobRunPhaseSucceeded, forgeletv1.JobRunPhaseFailed:
		if jr.Status.StartedAt == nil {
			t := metav1.NewTime(now)
			jr.Status.StartedAt = &t
		}
		if jr.Status.FinishedAt == nil {
			t := metav1.NewTime(now)
			jr.Status.FinishedAt = &t
		}
	}
	meta.SetStatusCondition(&jr.Status.Conditions, metav1.Condition{
		Type:               forgeletv1.JobRunConditionReady,
		Status:             metav1.ConditionFalse,
		Reason:             forgeletv1.ReasonProgressing,
		Message:            "observed phase " + phase,
		ObservedGeneration: jr.Generation,
	})

	if err := r.Status().Update(ctx, jr); err != nil {
		return ctrl.Result{}, fmt.Errorf("update jobrun %s status: %w", jr.Name, err)
	}
	id, err := IDFromCRName(jr.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.Projection.ApplyObserved(ctx, id, toModelPhase(phase), now); err != nil {
		return ctrl.Result{}, fmt.Errorf("project observed %s for %s: %w", phase, jr.Name, err)
	}
	return ctrl.Result{}, nil
}

func (r *JobRunReconciler) updateConditions(ctx context.Context, jr *forgeletv1.JobRun, status metav1.ConditionStatus, reason, message string) error {
	if c := meta.FindStatusCondition(jr.Status.Conditions, forgeletv1.JobRunConditionReady); c != nil &&
		c.Status == status && c.Reason == reason && c.Message == message {
		return nil
	}
	meta.SetStatusCondition(&jr.Status.Conditions, metav1.Condition{
		Type:               forgeletv1.JobRunConditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: jr.Generation,
	})
	if err := r.Status().Update(ctx, jr); err != nil {
		return fmt.Errorf("update jobrun %s conditions: %w", jr.Name, err)
	}
	return nil
}

// SetupWithManager registers the reconciler.
func (r *JobRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&forgeletv1.JobRun{}).
		Owns(&corev1.Pod{}).
		Complete(r)
}

// phaseRank orders observed phases; terminals share the top rank and are
// distinguished by the Terminal() check. Unknown or empty ranks lowest so a
// first observation always passes.
func phaseRank(phase string) int {
	switch phase {
	case forgeletv1.JobRunPhaseSucceeded, forgeletv1.JobRunPhaseFailed:
		return 2
	case forgeletv1.JobRunPhaseRunning:
		return 1
	default:
		return 0
	}
}

func mapPodPhase(p corev1.PodPhase) string {
	switch p {
	case corev1.PodRunning:
		return forgeletv1.JobRunPhaseRunning
	case corev1.PodSucceeded:
		return forgeletv1.JobRunPhaseSucceeded
	case corev1.PodFailed:
		return forgeletv1.JobRunPhaseFailed
	default:
		return forgeletv1.JobRunPhasePending
	}
}

func toModelPhase(phase string) model.ObservedPhase {
	switch phase {
	case forgeletv1.JobRunPhaseRunning:
		return model.PhaseRunning
	case forgeletv1.JobRunPhaseSucceeded:
		return model.PhaseSucceeded
	case forgeletv1.JobRunPhaseFailed:
		return model.PhaseFailed
	default:
		return model.PhasePending
	}
}

func buildPod(jr *forgeletv1.JobRun, class *forgeletv1.RunnerClass) *corev1.Pod {
	var automount = false
	var projectedTokenMode int32 = 420
	var tokenExpiration int64 = 3600

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jr.Name + PodSuffix,
			Namespace: jr.Namespace,
			Labels: map[string]string{
				appLabel:    "executor",
				jobRunLabel: jr.Name,
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy:                corev1.RestartPolicyNever,
			AutomountServiceAccountToken: &automount,
			ServiceAccountName:           ExecutorServiceAccount,
			NodeSelector:                 class.Spec.NodeSelector,
			Containers: []corev1.Container{
				{
					Name:       jobContainer,
					Image:      class.Spec.Image,
					Command:    []string{executorPath},
					Resources:  class.Spec.Resources,
					WorkingDir: workspaceDir,
					VolumeMounts: []corev1.VolumeMount{
						{Name: workspaceVolume, MountPath: workspaceDir},
						{Name: tokenVolume, MountPath: tokenMountDir, ReadOnly: true},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: workspaceVolume,
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				},
				{
					Name: tokenVolume,
					VolumeSource: corev1.VolumeSource{
						Projected: &corev1.ProjectedVolumeSource{
							Sources: []corev1.VolumeProjection{
								{
									ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
										Audience:          ControlPlaneAudience,
										ExpirationSeconds: &tokenExpiration,
										Path:              "token",
									},
								},
							},
							DefaultMode: &projectedTokenMode,
						},
					},
				},
			},
		},
	}
	return pod
}

// CRNameFromID mirrors model.JobRunID.CRName for CR construction outside the
// model package (kept protocol-consistent by test).
func CRNameFromID(id model.JobRunID) string {
	return id.CRName()
}

// IDFromCRName extracts the forgelet JobRun ID from a deterministic CR name.
func IDFromCRName(name string) (model.JobRunID, error) {
	const prefix = "jobrun-"
	if len(name) <= len(prefix) || name[:len(prefix)] != prefix {
		return "", fmt.Errorf("controller: CR name %q lacks %q prefix", name, prefix)
	}
	return model.JobRunID(name[len(prefix):]), nil
}
