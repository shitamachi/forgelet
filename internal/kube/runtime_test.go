package kube

import (
	"context"
	"errors"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/shitamachi/forgelet/internal/run"
)

func fixture(t *testing.T) (*Runtime, *fake.Clientset, run.Job, Profile) {
	t.Helper()
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "pods", func(a ktesting.Action) (bool, kruntime.Object, error) {
		pod := a.(ktesting.CreateAction).GetObject().(*corev1.Pod)
		pod.UID = types.UID("uid-1")
		return false, nil, nil
	})
	rt := &Runtime{Client: client, Namespace: "ci", ServerURL: "https://forgelet.ci.svc"}
	job := run.Job{ID: "job1", PodName: "f-job1", Plan: run.Plan{TimeoutSeconds: 1200}}
	profile := Profile{Image: "forgelet:dev", CPURequest: "100m", MemoryRequest: "64Mi", CPULimit: "1", MemoryLimit: "1Gi"}
	return rt, client, job, profile
}

func TestPodSecurityAndIdempotency(t *testing.T) {
	ctx := t.Context()
	rt, client, job, profile := fixture(t)
	uid, err := rt.Ensure(ctx, job, profile)
	if err != nil {
		t.Fatal(err)
	}
	if uid != "uid-1" {
		t.Fatal(uid)
	}
	job.PodUID = uid
	again, err := rt.Ensure(ctx, job, profile)
	if err != nil || again != uid {
		t.Fatalf("repeat ensure %q %v", again, err)
	}
	ps, err := client.CoreV1().Pods("ci").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ps.Items) != 1 {
		t.Fatal(len(ps.Items))
	}
	p := ps.Items[0]
	if p.Spec.AutomountServiceAccountToken == nil || *p.Spec.AutomountServiceAccountToken {
		t.Fatal("automatic API credentials mounted")
	}
	if p.Spec.ServiceAccountName != ServiceAccount || p.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Fatal("unexpected execution policy")
	}
	if p.Spec.SecurityContext.RunAsNonRoot == nil || !*p.Spec.SecurityContext.RunAsNonRoot {
		t.Fatal("root workload")
	}
	if *p.Spec.ActiveDeadlineSeconds != 1320 {
		t.Fatal("deadline")
	}
	c := p.Spec.Containers[0]
	if *c.SecurityContext.AllowPrivilegeEscalation || len(c.SecurityContext.Capabilities.Drop) != 1 {
		t.Fatal("privileges")
	}
	projection := p.Spec.Volumes[1].Projected.Sources[0].ServiceAccountToken
	if projection.Audience != Audience || *projection.ExpirationSeconds != 600 {
		t.Fatal("token scope")
	}
	for _, v := range p.Spec.Volumes {
		if v.HostPath != nil {
			t.Fatal("host path")
		}
	}
	job.PodUID = "another-uid"
	if _, err = rt.Ensure(ctx, job, profile); err == nil {
		t.Fatal("accepted replaced pod")
	}
	job.PodUID = "uid-1"
	if err = rt.Delete(ctx, job); err != nil {
		t.Fatal(err)
	}
	observation, err := rt.Observe(ctx, job)
	if err != nil || !observation.Missing {
		t.Fatal(observation, err)
	}
	if _, err = rt.Ensure(ctx, job, profile); err == nil {
		t.Fatal("recreated old attempt")
	}
}

func TestRuntimeRejectsInsecureURLAndInvalidResources(t *testing.T) {
	rt, _, job, profile := fixture(t)
	rt.ServerURL = "http://ci:8080"
	if _, err := rt.Ensure(t.Context(), job, profile); err == nil {
		t.Fatal("accepted insecure default")
	}
	rt.AllowInsecure = true
	if _, err := rt.Ensure(t.Context(), job, profile); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"bad", "-1", "0"} {
		p := profile
		p.MemoryLimit = bad
		if p.Validate() == nil {
			t.Fatal("accepted", bad)
		}
	}
}

func TestWorkloadIdentity(t *testing.T) {
	tests := []struct {
		name          string
		username      string
		audiences     []string
		uid           string
		authenticated bool
		wantOK        bool
	}{
		{"valid", "system:serviceaccount:ci:" + ServiceAccount, []string{Audience}, "uid-1", true, true},
		{"wrong audience", "system:serviceaccount:ci:" + ServiceAccount, []string{"kubernetes"}, "uid-1", true, false},
		{"wrong namespace", "system:serviceaccount:other:" + ServiceAccount, []string{Audience}, "uid-1", true, false},
		{"wrong service account", "system:serviceaccount:ci:default", []string{Audience}, "uid-1", true, false},
		{"wrong pod UID", "system:serviceaccount:ci:" + ServiceAccount, []string{Audience}, "other", true, false},
		{"unauthenticated", "system:serviceaccount:ci:" + ServiceAccount, []string{Audience}, "uid-1", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt, client, job, profile := fixture(t)
			if _, err := rt.Ensure(t.Context(), job, profile); err != nil {
				t.Fatal(err)
			}
			client.PrependReactor("create", "tokenreviews", func(a ktesting.Action) (bool, kruntime.Object, error) {
				review := a.(ktesting.CreateAction).GetObject().(*authenticationv1.TokenReview)
				if len(review.Spec.Audiences) != 1 || review.Spec.Audiences[0] != Audience {
					t.Fatal("missing explicit audience")
				}
				return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{Authenticated: tt.authenticated, Audiences: tt.audiences, User: authenticationv1.UserInfo{Username: tt.username, Extra: map[string]authenticationv1.ExtraValue{"authentication.kubernetes.io/pod-name": {job.PodName}, "authentication.kubernetes.io/pod-uid": {tt.uid}}}}}, nil
			})
			who, err := rt.Authenticate(t.Context(), "opaque-token")
			if tt.wantOK {
				if err != nil || who.PodUID != "uid-1" {
					t.Fatal(who, err)
				}
			} else if !errors.Is(err, ErrIdentity) {
				t.Fatal("accepted wrong identity", err)
			}
		})
	}
}

func TestAuthenticateHonorsContext(t *testing.T) {
	rt, client, _, _ := fixture(t)
	client.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, kruntime.Object, error) { return true, nil, context.Canceled })
	_, err := rt.Authenticate(t.Context(), "token")
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// A terminating Pod may flush logs/results, but must retain its exact identity.
func TestTerminatingIdentityAndForeignOwnership(t *testing.T) {
	rt, client, job, profile := fixture(t)
	ctx := t.Context()
	uid, err := rt.Ensure(ctx, job, profile)
	if err != nil {
		t.Fatal(err)
	}
	job.PodUID = uid
	client.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, kruntime.Object, error) {
		return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{Authenticated: true, Audiences: []string{Audience}, User: authenticationv1.UserInfo{Username: "system:serviceaccount:ci:" + ServiceAccount, Extra: map[string]authenticationv1.ExtraValue{"authentication.kubernetes.io/pod-name": {job.PodName}, "authentication.kubernetes.io/pod-uid": {uid}}}}}, nil
	})
	pod, err := client.CoreV1().Pods("ci").Get(ctx, job.PodName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	at := metav1.Now()
	pod.DeletionTimestamp = &at
	if _, err := client.CoreV1().Pods("ci").Update(ctx, pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	who, err := rt.Authenticate(ctx, "token")
	if err != nil || !who.Terminating || who.JobID != job.ID {
		t.Fatal(who, err)
	}
	pod.Labels[Label] = "different-job"
	if _, err := client.CoreV1().Pods("ci").Update(ctx, pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Observe(ctx, job); !errors.Is(err, ErrPermanent) {
		t.Fatal(err)
	}
	if err := rt.Delete(ctx, job); !errors.Is(err, ErrPermanent) {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Pods("ci").Get(ctx, job.PodName, metav1.GetOptions{}); err != nil {
		t.Fatal("foreign pod was deleted", err)
	}
}
func TestRuntimeFailureClassificationAndTimeoutPolicy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		permanent bool
	}{
		{"forbidden", apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "pod", errors.New("denied")), true},
		{"invalid", apierrors.NewBadRequest("invalid field"), true},
		{"throttled", apierrors.NewTooManyRequests("slow down", 1), false},
		{"unavailable", apierrors.NewServiceUnavailable("offline"), false},
		{"transport", errors.New("connection reset"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, client, job, profile := fixture(t)
			client.PrependReactor("create", "pods", func(ktesting.Action) (bool, kruntime.Object, error) { return true, nil, tc.err })
			if _, err := rt.Ensure(t.Context(), job, profile); err == nil || errors.Is(err, ErrPermanent) != tc.permanent {
				t.Fatal(err)
			}
		})
	}
	rt, client, job, profile := fixture(t)
	job.Plan.TimeoutSeconds = 0
	profile.ControlTimeout = 8 * time.Minute
	if _, err := rt.Ensure(t.Context(), job, profile); err != nil {
		t.Fatal(err)
	}
	pod, err := client.CoreV1().Pods("ci").Get(t.Context(), job.PodName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if *pod.Spec.ActiveDeadlineSeconds != 1320 || *pod.Spec.TerminationGracePeriodSeconds < 30 {
		t.Fatal(pod.Spec)
	}
	found := false
	for _, v := range pod.Spec.Containers[0].Env {
		if v.Name == "FORGELET_CONTROL_TIMEOUT" && v.Value == "8m0s" {
			found = true
		}
	}
	if !found {
		t.Fatal("profile control timeout missing")
	}
	profile.ControlTimeout = 2 * time.Hour
	if profile.Validate() == nil {
		t.Fatal("unbounded outage timeout")
	}
}
