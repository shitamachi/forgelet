package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	forgeletv1 "github.com/shitamachi/forgelet/api/v1alpha1"
	"github.com/shitamachi/forgelet/internal/run/model"
)

// countingProjection records ApplyObserved calls for idempotency assertions.
type countingProjection struct {
	calls []struct {
		id    model.JobRunID
		phase model.ObservedPhase
	}
}

func (p *countingProjection) ApplyObserved(_ context.Context, id model.JobRunID, phase model.ObservedPhase, _ time.Time) error {
	p.calls = append(p.calls, struct {
		id    model.JobRunID
		phase model.ObservedPhase
	}{id, phase})
	return nil
}

func (p *countingProjection) countOf(id model.JobRunID, phase model.ObservedPhase) int {
	n := 0
	for _, c := range p.calls {
		if c.id == id && c.phase == phase {
			n++
		}
	}
	return n
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := forgeletv1.AddToScheme(s); err != nil {
		t.Fatalf("forgelet scheme: %v", err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("core scheme: %v", err)
	}
	return s
}

const (
	testNS   = "forgelet-jobs"
	testULID = "01JTEST0000000000000000ABC"
)

func testJobRun(name string) *forgeletv1.JobRun {
	return &forgeletv1.JobRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec: forgeletv1.JobRunSpec{
			RunID:       "01JRUN000000000000000000X",
			JobKey:      "test",
			RunnerClass: "k3s-small",
			PlanID:      "01JTEST0000000000000000ABC",
			PlanDigest:  "d1",
			Attempt:     1,
		},
	}
}

func testRunnerClass() *forgeletv1.RunnerClass {
	return &forgeletv1.RunnerClass{
		ObjectMeta: metav1.ObjectMeta{Name: "k3s-small", Namespace: testNS},
		Spec: forgeletv1.RunnerClassSpec{
			Image:        "registry.example.com/ci/ubuntu:24.04",
			NodeSelector: map[string]string{"kubernetes.io/arch": "arm64"},
		},
	}
}

func newReconciler(t *testing.T, objs ...client.Object) (*JobRunReconciler, client.Client, *countingProjection) {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithStatusSubresource(&forgeletv1.JobRun{}, &corev1.Pod{}).
		WithObjects(objs...).
		Build()
	proj := &countingProjection{}
	r := &JobRunReconciler{
		Client:     c,
		Scheme:     testScheme(t),
		Projection: proj,
		Now:        func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
	}
	return r, c, proj
}

func reconcile(t *testing.T, r *JobRunReconciler, name string) {
	t.Helper()
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNS, Name: name}})
	if err != nil {
		t.Fatalf("reconcile %s: %v", name, err)
	}
}

func getPod(t *testing.T, c client.Client, name string) (*corev1.Pod, error) {
	t.Helper()
	var pod corev1.Pod
	err := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: name}, &pod)
	return &pod, err
}

// AC-M0 1: N reconciles create exactly one pod with the full invariant set.
func TestReconcileCreatesExactlyOneWellFormedPod(t *testing.T) {
	jr := testJobRun("jobrun-" + strings.ToLower(testULID))
	r, c, _ := newReconciler(t, jr, testRunnerClass())

	for i := 0; i < 5; i++ {
		reconcile(t, r, jr.Name)
	}

	pod, err := getPod(t, c, jr.Name+PodSuffix)
	if err != nil {
		t.Fatalf("pod missing: %v", err)
	}

	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Error("pod must set automountServiceAccountToken=false")
	}
	if pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("restartPolicy = %s, want Never", pod.Spec.RestartPolicy)
	}
	if pod.Spec.ServiceAccountName != ExecutorServiceAccount {
		t.Errorf("serviceAccountName = %q, want %q", pod.Spec.ServiceAccountName, ExecutorServiceAccount)
	}
	if len(pod.OwnerReferences) != 1 || pod.OwnerReferences[0].Kind != "JobRun" ||
		pod.OwnerReferences[0].Name != jr.Name {
		t.Errorf("ownerRef missing or wrong: %+v", pod.OwnerReferences)
	}
	if pod.Spec.NodeSelector["kubernetes.io/arch"] != "arm64" {
		t.Errorf("nodeSelector not from RunnerClass: %+v", pod.Spec.NodeSelector)
	}
	if len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Image != "registry.example.com/ci/ubuntu:24.04" {
		t.Errorf("container image not from RunnerClass: %+v", pod.Spec.Containers)
	}

	// Audience-bound projected token volume.
	var token *corev1.ServiceAccountTokenProjection
	for _, v := range pod.Spec.Volumes {
		if v.Projected != nil {
			for _, src := range v.Projected.Sources {
				if src.ServiceAccountToken != nil {
					token = src.ServiceAccountToken
				}
			}
		}
	}
	if token == nil || token.Audience != ControlPlaneAudience {
		t.Errorf("missing %q audience token projection: %+v", ControlPlaneAudience, token)
	}
	if token != nil && (token.ExpirationSeconds == nil || *token.ExpirationSeconds > 3600) {
		t.Errorf("token expiry must be <=1h: %+v", token.ExpirationSeconds)
	}

	// Workspace emptyDir mounted at /workspace.
	var ws *corev1.VolumeMount
	for _, vm := range pod.Spec.Containers[0].VolumeMounts {
		if vm.MountPath == "/workspace" {
			ws = &vm
		}
	}
	if ws == nil {
		t.Fatal("no /workspace mount")
	}
}

// AC-M0 2: pod phase matrix projects once per transition.
func TestObservePodPhaseMatrix(t *testing.T) {
	jr := testJobRun("jobrun-" + strings.ToLower(testULID))
	r, c, proj := newReconciler(t, jr, testRunnerClass())
	reconcile(t, r, jr.Name)
	// The CR name protocol lowercases the ULID (model.JobRunID.CRName).
	id := model.JobRunID(strings.ToLower(testULID))

	pod, _ := getPod(t, c, jr.Name+PodSuffix)

	setPhase := func(phase corev1.PodPhase) {
		pod.Status.Phase = phase
		if err := c.Status().Update(context.Background(), pod); err != nil {
			t.Fatalf("pod status update: %v", err)
		}
		reconcile(t, r, jr.Name)
		// Reconcile twice: the second must not re-project the same phase.
		reconcile(t, r, jr.Name)
	}

	setPhase(corev1.PodRunning)
	if got := phaseOf(t, c, jr.Name); got != forgeletv1.JobRunPhaseRunning {
		t.Fatalf("phase = %s, want running", got)
	}
	if n := proj.countOf(id, model.PhaseRunning); n != 1 {
		t.Errorf("running projected %d times, want 1", n)
	}

	setPhase(corev1.PodPending) // stale observation must not move state back
	if got := phaseOf(t, c, jr.Name); got != forgeletv1.JobRunPhaseRunning {
		t.Errorf("stale pending regressed phase to %s", got)
	}

	setPhase(corev1.PodSucceeded)
	if got := phaseOf(t, c, jr.Name); got != forgeletv1.JobRunPhaseSucceeded {
		t.Fatalf("phase = %s, want succeeded", got)
	}
	if n := proj.countOf(id, model.PhaseSucceeded); n != 1 {
		t.Errorf("succeeded projected %d times, want 1", n)
	}

	var updated forgeletv1.JobRun
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: jr.Name}, &updated); err != nil {
		t.Fatalf("get: %v", err)
	}
	if updated.Status.FinishedAt == nil || updated.Status.StartedAt == nil {
		t.Error("terminal phase must set started/finished timestamps")
	}
}

func phaseOf(t *testing.T, c client.Client, name string) string {
	t.Helper()
	var jr forgeletv1.JobRun
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: name}, &jr); err != nil {
		t.Fatalf("get jobrun: %v", err)
	}
	return jr.Status.Phase
}

// AC-M0 2: terminal JobRun never recreates a deleted (GC'ed) pod.
func TestTerminalDoesNotRecreatePod(t *testing.T) {
	jr := testJobRun("jobrun-" + strings.ToLower(testULID))
	r, c, _ := newReconciler(t, jr, testRunnerClass())
	reconcile(t, r, jr.Name)

	// Force terminal status directly.
	var updated forgeletv1.JobRun
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: jr.Name}, &updated); err != nil {
		t.Fatalf("get: %v", err)
	}
	updated.Status.Phase = forgeletv1.JobRunPhaseSucceeded
	if err := c.Status().Update(context.Background(), &updated); err != nil {
		t.Fatalf("status: %v", err)
	}

	// Delete the pod (GC semantics) and reconcile again.
	if err := c.Delete(context.Background(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: jr.Name + PodSuffix, Namespace: testNS}}); err != nil {
		t.Fatalf("delete pod: %v", err)
	}
	reconcile(t, r, jr.Name)
	if _, err := getPod(t, c, jr.Name+PodSuffix); !apierrors.IsNotFound(err) {
		t.Fatalf("terminal JobRun recreated its pod: %v", err)
	}
}

// AC-M0 3: missing RunnerClass sets a condition, creates no pod, recovers.
func TestMissingRunnerClass(t *testing.T) {
	jr := testJobRun("jobrun-" + strings.ToLower(testULID))
	r, c, _ := newReconciler(t, jr) // no RunnerClass object

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNS, Name: jr.Name}}); err != nil {
		t.Fatalf("reconcile with missing class must not error: %v", err)
	}
	if _, err := getPod(t, c, jr.Name+PodSuffix); !apierrors.IsNotFound(err) {
		t.Fatal("pod created despite missing RunnerClass")
	}
	var updated forgeletv1.JobRun
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: jr.Name}, &updated); err != nil {
		t.Fatalf("get: %v", err)
	}
	cond := meta.FindStatusCondition(updated.Status.Conditions, forgeletv1.JobRunConditionReady)
	if cond == nil || cond.Reason != forgeletv1.ReasonRunnerClassMissing {
		t.Fatalf("missing RunnerClassMissing condition: %+v", cond)
	}

	// Class appears; next reconcile creates the pod.
	if err := c.Create(context.Background(), testRunnerClass()); err != nil {
		t.Fatalf("create class: %v", err)
	}
	reconcile(t, r, jr.Name)
	if _, err := getPod(t, c, jr.Name+PodSuffix); err != nil {
		t.Fatalf("pod not created after class appeared: %v", err)
	}
}

// AC-M0 4: ActiveStore create-or-get and cascading delete.
type staticSource struct{ rec model.JobRunRecord }

func (s staticSource) Get(context.Context, model.JobRunID) (model.JobRunRecord, error) {
	return s.rec, nil
}

func TestActiveStoreCreateOrGetAndDelete(t *testing.T) {
	// The CR name protocol lowercases the ULID (model.JobRunID.CRName).
	id := model.JobRunID(strings.ToLower(testULID))
	rec := model.JobRunRecord{
		ID: id, RunID: "01JRUN000000000000000000X", JobKey: "test",
		RunnerClass: "k3s-small", PlanDigest: "d1", Attempt: 1,
	}
	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithStatusSubresource(&forgeletv1.JobRun{}, &corev1.Pod{}).
		Build()
	store := NewActiveStore(c, staticSource{rec}, testNS)
	ctx := context.Background()

	obj1, err := store.CreateOrGet(ctx, id)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	obj2, err := store.CreateOrGet(ctx, id)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if obj1 != obj2 {
		t.Fatalf("create-or-get returned different objects: %+v vs %+v", obj1, obj2)
	}

	// Protocol consistency with model.JobRunID.CRName (AC-M0 5).
	if obj1.Name != id.CRName() {
		t.Fatalf("CR name %q != model CRName %q", obj1.Name, id.CRName())
	}

	// Pod owned by the CR declares the cascade relationship.
	cr := &forgeletv1.JobRun{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: testNS, Name: obj1.Name}, cr); err != nil {
		t.Fatalf("get CR: %v", err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: cr.Name + PodSuffix, Namespace: testNS}}
	if err := ctrl.SetControllerReference(cr, pod, testScheme(t)); err != nil {
		t.Fatalf("owner ref: %v", err)
	}
	if err := c.Create(ctx, pod); err != nil {
		t.Fatalf("create pod: %v", err)
	}

	if err := store.Delete(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := store.Delete(ctx, id); err != nil {
		t.Fatalf("delete must be idempotent: %v", err)
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: testNS, Name: obj1.Name}, &forgeletv1.JobRun{}); !apierrors.IsNotFound(err) {
		t.Fatalf("CR not deleted: %v", err)
	}
}

func TestCRNameProtocol(t *testing.T) {
	if _, err := IDFromCRName("bogus"); err == nil {
		t.Fatal("non-protocol name accepted")
	}
	id, err := IDFromCRName("jobrun-01jabc")
	if err != nil || id != model.JobRunID("01jabc") {
		t.Fatalf("IDFromCRName roundtrip: %v %v", id, err)
	}
	// Round trip with the model function.
	for _, raw := range []model.JobRunID{"01JABC", "01Jxyz0000000000000000zz"} {
		got, err := IDFromCRName(CRNameFromID(raw))
		if err != nil || got != model.JobRunID(strings.ToLower(string(raw))) {
			t.Errorf("roundtrip failed for %s: %v %v", raw, got, err)
		}
	}
}
