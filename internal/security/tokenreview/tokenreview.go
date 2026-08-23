// Package tokenreview verifies executor identities by presenting the caller's
// bearer token to the Kubernetes TokenReview API. It is the production
// verifier for the projected, audience-bound ServiceAccount tokens that
// JobRun pods mount (spec 0003 FR-S1.5, pod projection per 0004 §4).
//
// The projected token authenticates a pod; it does not carry forgelet
// claims. This adapter therefore binds the verified pod to exactly one
// JobRun through BindingSource — without that binding an executor could
// read another job's plan or secrets (FR-S1.3).
package tokenreview

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/security/identity"
)

// Extras set by TokenReview for tokens bound to a pod. Kubernetes 1.34
// renamed the prefix from authentication.k8s.io to authentication.kubernetes.io
// (both appear in the wild, so both are accepted).
const (
	PodUIDExtraKey  = "authentication.k8s.io/pod-uid"
	PodNameExtraKey = "authentication.k8s.io/pod-name"

	podUIDExtraKeyNew  = "authentication.kubernetes.io/pod-uid"
	podNameExtraKeyNew = "authentication.kubernetes.io/pod-name"
)

// ErrInvalidToken re-exported for callers matching on verification failure.
var ErrInvalidToken = identity.ErrInvalidToken

// BindingSource resolves which JobRun a verified pod executes. Any error
// rejects the token; the Verifier classifies it as a verification failure.
// The cluster adapter (pod lookup by UID) lands with the k3s smoke task
// (0011 T8); tests and single-process deployments use InMemoryBindings.
type BindingSource interface {
	JobRunForPod(ctx context.Context, namespace, podName, podUID string) (model.JobRunID, error)
}

// InMemoryBindings is an exact-match pod-UID → JobRun table.
type InMemoryBindings struct {
	byUID map[string]model.JobRunID
}

// NewInMemoryBindings indexes the given pods by UID.
func NewInMemoryBindings(pods map[string]model.JobRunID) *InMemoryBindings {
	return &InMemoryBindings{byUID: pods}
}

// JobRunForPod implements BindingSource. An unknown pod never maps.
func (b *InMemoryBindings) JobRunForPod(_ context.Context, _, _, podUID string) (model.JobRunID, error) {
	id, ok := b.byUID[podUID]
	if !ok {
		return "", fmt.Errorf("pod %q is not bound to any job run", podUID)
	}
	return id, nil
}

// Verifier implements identity.Verifier via the TokenReview API. JWKS-style
// local validation was rejected: it would force key distribution onto every
// control-plane replica, while TokenReview needs only API-server access the
// server already has.
type Verifier struct {
	Client    kubernetes.Interface
	Audience  string // required token audience (projected volume setting)
	Namespace string // expected executor namespace ("forgelet-jobs")
	SAName    string // expected service account ("forgelet-executor")
	Scopes    []string
	Bindings  BindingSource
	Now       func() time.Time

	now func() time.Time
}

// Verify implements identity.Verifier.
func (v *Verifier) Verify(ctx context.Context, raw string) (identity.Identity, error) {
	if v.now == nil {
		v.now = time.Now
	}
	if v.Audience == "" || v.Namespace == "" || v.SAName == "" || v.Bindings == nil {
		return identity.Identity{}, errors.New("tokenreview: Audience, Namespace, SAName and Bindings are required")
	}
	review := &authenticationv1.TokenReview{
		Spec: authenticationv1.TokenReviewSpec{Token: raw, Audiences: []string{v.Audience}},
	}
	res, err := v.Client.AuthenticationV1().TokenReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return identity.Identity{}, fmt.Errorf("%w: review failed: %w", ErrInvalidToken, err)
	}
	if !res.Status.Authenticated {
		detail := res.Status.Error
		if detail == "" {
			detail = "token not authenticated"
		}
		return identity.Identity{}, fmt.Errorf("%w: %s", ErrInvalidToken, detail)
	}
	ns, sa, err := parseServiceAccount(res.Status.User.Username)
	if err != nil {
		return identity.Identity{}, err
	}
	if ns != v.Namespace {
		return identity.Identity{}, fmt.Errorf("%w: namespace %q is not an executor namespace", ErrInvalidToken, ns)
	}
	if sa != v.SAName {
		return identity.Identity{}, fmt.Errorf("%w: service account %q may not execute jobs", ErrInvalidToken, sa)
	}
	podUID, podName, err := extras(res.Status.User.Extra)
	if err != nil {
		return identity.Identity{}, err
	}
	jobRunID, err := v.Bindings.JobRunForPod(ctx, ns, podName, podUID)
	if err != nil {
		return identity.Identity{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	// TokenReviewStatus carries no expiration: an expired or not-yet-valid
	// token simply fails authentication above, so expiry is enforced by the
	// API server itself (FR-S1.2). The projected volume caps lifetime at 1h
	// (0004 §4); IssuedAt reflects verification time, not minting time.
	return identity.Identity{
		Audience:  v.Audience,
		Namespace: ns,
		PodUID:    podUID,
		JobRunID:  jobRunID,
		Scopes:    append([]string(nil), v.Scopes...),
		TokenID:   "sa:" + sa + ":" + podUID,
		IssuedAt:  v.now(),
	}, nil
}

func parseServiceAccount(username string) (ns, sa string, err error) {
	const prefix = "system:serviceaccount:"
	rest, ok := strings.CutPrefix(username, prefix)
	if !ok {
		return "", "", fmt.Errorf("%w: user %q is not a service account", ErrInvalidToken, username)
	}
	ns, sa, ok = strings.Cut(rest, ":")
	if !ok || ns == "" || sa == "" || strings.Contains(sa, ":") {
		return "", "", fmt.Errorf("%w: malformed service account username %q", ErrInvalidToken, username)
	}
	return ns, sa, nil
}

func extras(extra map[string]authenticationv1.ExtraValue) (podUID, podName string, err error) {
	lookup := func(oldKey, newKey string) (string, bool) {
		for _, key := range []string{oldKey, newKey} {
			if vals := extra[key]; len(vals) == 1 {
				return vals[0], true
			}
		}
		return "", false
	}
	var ok bool
	if podUID, ok = lookup(PodUIDExtraKey, podUIDExtraKeyNew); !ok {
		return "", "", fmt.Errorf("%w: no pod uid extra", ErrInvalidToken)
	}
	if podName, ok = lookup(PodNameExtraKey, podNameExtraKeyNew); !ok {
		return "", "", fmt.Errorf("%w: no pod name extra", ErrInvalidToken)
	}
	return podUID, podName, nil
}
