package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	forgeletv1 "github.com/shitamachi/forgelet/api/v1alpha1"
	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/run/scheduler"
)

// ActiveStore adapts the 0002 ActiveExecutionStore port to Kubernetes:
// CreateOrGet materializes the deterministic JobRun CR, Delete removes it
// (cascading to the pod via the owner reference).
type ActiveStore struct {
	client    client.Client
	source    JobRunSource
	namespace string
}

// NewActiveStore wires an ActiveStore operating in the given namespace.
func NewActiveStore(c client.Client, source JobRunSource, namespace string) *ActiveStore {
	return &ActiveStore{client: c, source: source, namespace: namespace}
}

// DurableJobRunSource adapts a scheduler.DurableStore to the JobRunSource
// port used when the control plane dispatches into Kubernetes.
type DurableJobRunSource struct{ Durable scheduler.DurableStore }

// Get implements JobRunSource.
func (s DurableJobRunSource) Get(ctx context.Context, id model.JobRunID) (model.JobRunRecord, error) {
	return s.Durable.GetJobRun(ctx, id)
}

// CreateOrGet implements scheduler.ActiveExecutionStore.
func (a *ActiveStore) CreateOrGet(ctx context.Context, id model.JobRunID) (scheduler.ActiveObject, error) {
	name := CRNameFromID(id)
	var existing forgeletv1.JobRun
	err := a.client.Get(ctx, types.NamespacedName{Namespace: a.namespace, Name: name}, &existing)
	if err == nil {
		return scheduler.ActiveObject{Name: existing.Name, UID: string(existing.UID)}, nil
	}
	if !apierrors.IsNotFound(err) {
		return scheduler.ActiveObject{}, fmt.Errorf("get jobrun %s: %w", name, err)
	}

	rec, err := a.source.Get(ctx, id)
	if err != nil {
		return scheduler.ActiveObject{}, fmt.Errorf("job run source %s: %w", id, err)
	}
	cr := &forgeletv1.JobRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: a.namespace},
		Spec: forgeletv1.JobRunSpec{
			RunID:       string(rec.RunID),
			JobKey:      rec.JobKey,
			RunnerClass: rec.RunnerClass,
			PlanID:      string(rec.ID),
			PlanDigest:  rec.PlanDigest,
			Attempt:     int32(rec.Attempt),
		},
	}
	if err := a.client.Create(ctx, cr); err != nil {
		if apierrors.IsAlreadyExists(err) {
			var again forgeletv1.JobRun
			if gerr := a.client.Get(ctx, types.NamespacedName{Namespace: cr.Namespace, Name: name}, &again); gerr == nil {
				return scheduler.ActiveObject{Name: again.Name, UID: string(again.UID)}, nil
			}
		}
		return scheduler.ActiveObject{}, fmt.Errorf("create jobrun %s: %w", name, err)
	}
	return scheduler.ActiveObject{Name: cr.Name, UID: string(cr.UID)}, nil
}

// Delete implements scheduler.ActiveExecutionStore; deleting a missing CR
// succeeds (idempotent). The pod cascades via ownerRef.
func (a *ActiveStore) Delete(ctx context.Context, id model.JobRunID) error {
	name := CRNameFromID(id)
	err := a.client.Delete(ctx, &forgeletv1.JobRun{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: a.namespace}})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete jobrun %s: %w", name, err)
	}
	return nil
}
