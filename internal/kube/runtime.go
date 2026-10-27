// Package kube adapts Kubernetes Pods to forgelet job attempts.
package kube

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/shitamachi/forgelet/internal/run"
)

const (
	Label          = "forgelet.dev/job"
	Audience       = "forgelet"
	ServiceAccount = "forgelet-executor"
)

var ErrIdentity = errors.New("invalid workload identity")
var ErrPermanent = errors.New("permanent runtime failure")

func runtimeError(err error) error {
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) || apierrors.IsInvalid(err) || apierrors.IsBadRequest(err) || apierrors.IsMethodNotSupported(err) {
		return fmt.Errorf("%w: %w", ErrPermanent, err)
	}
	return err
}

type Profile struct {
	Act            *ActProfile       `yaml:"act,omitempty" json:"act,omitempty"`
	ControlTimeout time.Duration     `yaml:"control_plane_timeout,omitempty" json:"control_plane_timeout,omitempty"`
	Build          *BuildProfile     `yaml:"build,omitempty" json:"build,omitempty"`
	Image          string            `yaml:"image" json:"image"`
	CPURequest     string            `yaml:"cpu_request" json:"cpu_request"`
	MemoryRequest  string            `yaml:"memory_request" json:"memory_request"`
	CPULimit       string            `yaml:"cpu_limit" json:"cpu_limit"`
	MemoryLimit    string            `yaml:"memory_limit" json:"memory_limit"`
	WorkspaceLimit string            `yaml:"workspace_limit,omitempty" json:"workspace_limit,omitempty"`
	NodeSelector   map[string]string `yaml:"node_selector" json:"node_selector"`
}

func (p Profile) Validate() error {
	if p.Act != nil {
		if p.Build != nil {
			return errors.New("act and native BuildKit profiles cannot be combined")
		}
		if err := p.Act.Validate(); err != nil {
			return err
		}
		runner := p.Image
		if p.Act.RunnerImage != "" {
			runner = p.Act.RunnerImage
		}
		if err := run.ValidateImageMirrors(runner, p.Act.ImageMirrors); err != nil {
			return err
		}
	}
	if p.ControlTimeout < 0 || p.ControlTimeout > time.Hour {
		return errors.New("control_plane_timeout must be between zero and one hour")
	}
	if p.Build != nil {
		if err := p.Build.Validate(); err != nil {
			return err
		}
	}
	if strings.TrimSpace(p.Image) == "" {
		return errors.New("profile image is required")
	}
	for name, value := range map[string]string{"cpu_request": p.CPURequest, "memory_request": p.MemoryRequest, "cpu_limit": p.CPULimit, "memory_limit": p.MemoryLimit, "workspace_limit": p.WorkspaceLimit} {
		if value == "" {
			continue
		}
		q, err := resource.ParseQuantity(value)
		if err != nil || q.Sign() <= 0 {
			return fmt.Errorf("invalid %s", name)
		}
	}
	return nil
}

type Identity struct {
	Terminating bool
	JobID       string
	PodName     string
	PodUID      string
}

type Observation struct {
	UID     string
	Phase   corev1.PodPhase
	Missing bool
	Reason  string
}

type Runtime struct {
	Client        kubernetes.Interface
	Namespace     string
	ServerURL     string
	AllowInsecure bool
	// CASecret is an optional Kubernetes Secret containing ca.crt for the runtime API.
	CASecret string
}

func (r *Runtime) Ensure(ctx context.Context, job run.Job, profile Profile) (string, error) {
	if err := profile.Validate(); err != nil {
		return "", fmt.Errorf("%w: %w", ErrPermanent, err)
	}
	parsed, err := url.Parse(r.ServerURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && (!r.AllowInsecure || parsed.Scheme != "http")) {
		return "", fmt.Errorf("%w: runtime URL must use HTTPS (HTTP requires explicit development override)", ErrPermanent)
	}
	pod, err := r.Client.CoreV1().Pods(r.Namespace).Get(ctx, job.PodName, metav1.GetOptions{})
	if err == nil {
		return ownedUID(pod, job)
	}
	if !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("get execution pod: %w", runtimeError(err))
	}
	if job.PodUID != "" {
		return "", fmt.Errorf("%w: cannot recreate an attempt with a recorded pod UID", ErrPermanent)
	}
	no := false
	yes := true
	uid := int64(1000)
	grace := int64(45)
	expiry := int64(600)
	deadline := int64(job.Plan.TimeoutSeconds + 120)
	if job.Plan.TimeoutSeconds <= 0 {
		deadline = 1320
	}
	if profile.Act != nil {
		deadline += run.ActPreparationSeconds
	}
	resources := corev1.ResourceRequirements{Requests: corev1.ResourceList{}, Limits: corev1.ResourceList{}}
	workspaceLimit := profile.WorkspaceLimit
	if workspaceLimit == "" {
		workspaceLimit = "2Gi"
	}
	for name, value := range map[corev1.ResourceName]string{corev1.ResourceCPU: profile.CPURequest, corev1.ResourceMemory: profile.MemoryRequest} {
		if value != "" {
			resources.Requests[name] = resource.MustParse(value)
		}
	}
	for name, value := range map[corev1.ResourceName]string{corev1.ResourceCPU: profile.CPULimit, corev1.ResourceMemory: profile.MemoryLimit} {
		if value != "" {
			resources.Limits[name] = resource.MustParse(value)
		}
	}
	pod = &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: job.PodName, Namespace: r.Namespace, Labels: map[string]string{Label: job.ID, "app.kubernetes.io/managed-by": "forgelet"}},
		Spec: corev1.PodSpec{
			ServiceAccountName: ServiceAccount, AutomountServiceAccountToken: &no, RestartPolicy: corev1.RestartPolicyNever,
			ActiveDeadlineSeconds: &deadline, TerminationGracePeriodSeconds: &grace, NodeSelector: profile.NodeSelector,
			SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: &yes, RunAsUser: &uid, RunAsGroup: &uid, FSGroup: &uid, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
			Containers: []corev1.Container{{Name: "executor", Image: profile.Image, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"/usr/local/bin/forgelet-executor"},
				// emptyDir's root belongs to root even with fsGroup. Execute creates
				// the child workspace as the executor UID, so Git trusts its owner.
				WorkingDir: "/workspace", Resources: resources,
				Env:             []corev1.EnvVar{{Name: "FORGELET_URL", Value: r.ServerURL}, {Name: "JOB_ID", Value: job.ID}, {Name: "POD_UID", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.uid"}}}, {Name: "TOKEN_FILE", Value: "/var/run/forgelet/token"}, {Name: "WORKSPACE", Value: "/workspace/repository"}, {Name: "HOME", Value: "/workspace"}},
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &no, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				VolumeMounts:    []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}, {Name: "identity", MountPath: "/var/run/forgelet", ReadOnly: true}},
			}},
			Volumes: []corev1.Volume{{Name: "workspace", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: quantityPointer(workspaceLimit)}}}, {Name: "identity", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Audience: Audience, ExpirationSeconds: &expiry, Path: "token"}}}}}}},
		},
	}
	controlTimeout := profile.ControlTimeout
	if controlTimeout == 0 {
		controlTimeout = 5 * time.Minute
	}
	pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{Name: "FORGELET_CONTROL_TIMEOUT", Value: controlTimeout.String()})
	if r.CASecret != "" {
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{Name: "api-ca", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: r.CASecret}}})
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "api-ca", MountPath: "/var/run/forgelet-ca", ReadOnly: true})
		pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{Name: "SSL_CERT_FILE", Value: "/var/run/forgelet-ca/ca.crt"})
	}
	if job.Plan.NeedsBuild() {
		if profile.Build == nil {
			return "", fmt.Errorf("%w: execution profile does not enable BuildKit", ErrPermanent)
		}
		addBuilder(pod, *profile.Build)
	}
	if job.Plan.Kernel == "act" {
		if profile.Act == nil {
			return "", fmt.Errorf("%w: act job requires an explicit act profile", ErrPermanent)
		}
		addAct(pod, *profile.Act, profile.Image)
	}
	pod, err = r.Client.CoreV1().Pods(r.Namespace).Create(ctx, pod, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		pod, err = r.Client.CoreV1().Pods(r.Namespace).Get(ctx, job.PodName, metav1.GetOptions{})
	}
	if err != nil {
		return "", fmt.Errorf("create execution pod: %w", runtimeError(err))
	}
	return ownedUID(pod, job)
}

func quantityPointer(s string) *resource.Quantity { q := resource.MustParse(s); return &q }

func ownedUID(pod *corev1.Pod, job run.Job) (string, error) {
	if pod.Labels[Label] != job.ID || pod.Spec.ServiceAccountName != ServiceAccount || (job.PodUID != "" && string(pod.UID) != job.PodUID) {
		return "", fmt.Errorf("%w: execution pod ownership mismatch", ErrPermanent)
	}
	return string(pod.UID), nil
}

func (r *Runtime) Observe(ctx context.Context, job run.Job) (Observation, error) {
	p, err := r.Client.CoreV1().Pods(r.Namespace).Get(ctx, job.PodName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return Observation{Missing: true}, nil
	}
	if err != nil {
		return Observation{}, fmt.Errorf("observe pod: %w", runtimeError(err))
	}
	uid, err := ownedUID(p, job)
	if err != nil {
		return Observation{}, err
	}
	return Observation{UID: uid, Phase: p.Status.Phase, Reason: p.Status.Reason}, nil
}

func (r *Runtime) Delete(ctx context.Context, job run.Job) error {
	if job.PodUID == "" {
		return errors.New("cannot delete pod without recorded UID")
	}
	pod, err := r.Client.CoreV1().Pods(r.Namespace).Get(ctx, job.PodName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read pod before deletion: %w", runtimeError(err))
	}
	if _, err := ownedUID(pod, job); err != nil {
		return err
	}
	uid := types.UID(job.PodUID)
	err = r.Client.CoreV1().Pods(r.Namespace).Delete(ctx, job.PodName, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete execution pod: %w", runtimeError(err))
	}
	return nil
}

func (r *Runtime) Pods(ctx context.Context) (map[string]string, error) {
	ps, err := r.Client.CoreV1().Pods(r.Namespace).List(ctx, metav1.ListOptions{LabelSelector: Label})
	if err != nil {
		return nil, fmt.Errorf("list execution pods: %w", err)
	}
	result := make(map[string]string, len(ps.Items))
	for _, p := range ps.Items {
		result[p.Labels[Label]] = string(p.UID)
	}
	return result, nil
}

func (r *Runtime) Authenticate(ctx context.Context, token string) (Identity, error) {
	if token == "" || len(token) > 16384 {
		return Identity{}, ErrIdentity
	}
	review, err := r.Client.AuthenticationV1().TokenReviews().Create(ctx, &authenticationv1.TokenReview{Spec: authenticationv1.TokenReviewSpec{Token: token, Audiences: []string{Audience}}}, metav1.CreateOptions{})
	if err != nil {
		return Identity{}, fmt.Errorf("review workload token: %w", err)
	}
	s := review.Status
	audienceOK := false
	for _, a := range s.Audiences {
		if a == Audience {
			audienceOK = true
		}
	}
	names := s.User.Extra["authentication.kubernetes.io/pod-name"]
	uids := s.User.Extra["authentication.kubernetes.io/pod-uid"]
	if !s.Authenticated || !audienceOK || s.User.Username != "system:serviceaccount:"+r.Namespace+":"+ServiceAccount || len(names) != 1 || len(uids) != 1 {
		return Identity{}, ErrIdentity
	}
	p, err := r.Client.CoreV1().Pods(r.Namespace).Get(ctx, names[0], metav1.GetOptions{})
	if err != nil || string(p.UID) != uids[0] || p.Labels[Label] == "" || p.Spec.ServiceAccountName != ServiceAccount {
		return Identity{}, ErrIdentity
	}
	return Identity{Terminating: p.DeletionTimestamp != nil, JobID: p.Labels[Label], PodName: names[0], PodUID: uids[0]}, nil
}
