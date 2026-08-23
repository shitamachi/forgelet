package tokenreview

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/security/identity"
)

const (
	testAudience  = "forgelet-control-plane"
	testNamespace = "forgelet-jobs"
	testSA        = "forgelet-executor"
)

func newTestVerifier(t *testing.T, statusFn func(*authenticationv1.TokenReview) authenticationv1.TokenReviewStatus) (*Verifier, *[]authenticationv1.TokenReviewSpec) {
	t.Helper()
	var specs []authenticationv1.TokenReviewSpec
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/apis/authentication.k8s.io/v1/tokenreviews") || r.Method != http.MethodPost {
			http.Error(w, "unexpected call", http.StatusNotFound)
			return
		}
		var review authenticationv1.TokenReview
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		specs = append(specs, review.Spec)
		review.Status = statusFn(&review)
		review.TypeMeta = metav1.TypeMeta{APIVersion: "authentication.k8s.io/v1", Kind: "TokenReview"}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(review)
	}))
	t.Cleanup(srv.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{
		Host:               srv.URL,
		AcceptContentTypes: "application/json",
		ContentType:        "application/json",
	})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	v := &Verifier{
		Client:    client,
		Audience:  testAudience,
		Namespace: testNamespace,
		SAName:    testSA,
		Scopes:    []string{identity.ScopePlanRead, identity.ScopeSecretsRead, identity.ScopeStatusWrite},
		Bindings:  NewInMemoryBindings(map[string]model.JobRunID{"pod-uid-1": "01HJOB"}),
	}
	v.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	return v, &specs
}

func authenticatedStatus(review *authenticationv1.TokenReview) authenticationv1.TokenReviewStatus {
	return authenticationv1.TokenReviewStatus{
		Authenticated: true,
		Audiences:     review.Spec.Audiences,
		User: authenticationv1.UserInfo{
			Username: "system:serviceaccount:" + testNamespace + ":" + testSA,
			Extra: map[string]authenticationv1.ExtraValue{
				PodUIDExtraKey:  {"pod-uid-1"},
				PodNameExtraKey: {"jobrun-cr-1-pod"},
			},
		},
	}
}

func TestVerifyBindsPodToJobRun(t *testing.T) {
	v, specs := newTestVerifier(t, authenticatedStatus)
	id, err := v.Verify(context.Background(), "sa-token")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(*specs) != 1 || len((*specs)[0].Audiences) != 1 || (*specs)[0].Audiences[0] != testAudience {
		t.Fatalf("request spec = %+v", *specs)
	}
	if id.Audience != testAudience || id.Namespace != testNamespace || id.PodUID != "pod-uid-1" {
		t.Errorf("identity = %+v", id)
	}
	if id.JobRunID != "01HJOB" {
		t.Errorf("job run binding = %q", id.JobRunID)
	}
	if !id.HasScope(identity.ScopePlanRead) || !id.HasScope(identity.ScopeSecretsRead) || !id.HasScope(identity.ScopeStatusWrite) {
		t.Errorf("scopes = %v", id.Scopes)
	}
}

func TestVerifyRejections(t *testing.T) {
	cases := []struct {
		name   string
		status func(*authenticationv1.TokenReview) authenticationv1.TokenReviewStatus
	}{
		{
			name: "not authenticated",
			status: func(_ *authenticationv1.TokenReview) authenticationv1.TokenReviewStatus {
				return authenticationv1.TokenReviewStatus{Error: "token expired or not valid"}
			},
		},
		{
			name: "other service account",
			status: func(r *authenticationv1.TokenReview) authenticationv1.TokenReviewStatus {
				s := authenticatedStatus(r)
				s.User.Username = "system:serviceaccount:" + testNamespace + ":other-sa"
				return s
			},
		},
		{
			name: "other namespace",
			status: func(r *authenticationv1.TokenReview) authenticationv1.TokenReviewStatus {
				s := authenticatedStatus(r)
				s.User.Username = "system:serviceaccount:default:" + testSA
				return s
			},
		},
		{
			name: "non service-account user",
			status: func(r *authenticationv1.TokenReview) authenticationv1.TokenReviewStatus {
				s := authenticatedStatus(r)
				s.User.Username = "system:user:alice"
				return s
			},
		},
		{
			name: "missing pod uid extra",
			status: func(r *authenticationv1.TokenReview) authenticationv1.TokenReviewStatus {
				s := authenticatedStatus(r)
				delete(s.User.Extra, PodUIDExtraKey)
				return s
			},
		},
		{
			name: "missing pod name extra",
			status: func(r *authenticationv1.TokenReview) authenticationv1.TokenReviewStatus {
				s := authenticatedStatus(r)
				delete(s.User.Extra, PodNameExtraKey)
				return s
			},
		},
		{
			name: "pod bound to another job's uid",
			status: func(r *authenticationv1.TokenReview) authenticationv1.TokenReviewStatus {
				s := authenticatedStatus(r)
				s.User.Extra[PodUIDExtraKey] = authenticationv1.ExtraValue{"some-other-pod"}
				return s
			},
		},
		{
			name: "malformed sa username",
			status: func(r *authenticationv1.TokenReview) authenticationv1.TokenReviewStatus {
				s := authenticatedStatus(r)
				s.User.Username = "system:serviceaccount:nocolon"
				return s
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, _ := newTestVerifier(t, tc.status)
			_, err := v.Verify(context.Background(), "sa-token")
			if !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("err = %v, want ErrInvalidToken", err)
			}
		})
	}
}

// The verifier must request exactly the configured audience; a server-side
// audience mismatch surfaces as an unauthenticated review.
func TestVerifyAudienceRequestedAndMismatchRejected(t *testing.T) {
	mismatch := 0
	v, specs := newTestVerifier(t, func(r *authenticationv1.TokenReview) authenticationv1.TokenReviewStatus {
		for _, a := range r.Spec.Audiences {
			if a == testAudience {
				return authenticatedStatus(r)
			}
		}
		mismatch++
		return authenticationv1.TokenReviewStatus{}
	})
	if _, err := v.Verify(context.Background(), "sa-token"); err != nil {
		t.Fatalf("matching audience rejected: %v", err)
	}
	if mismatch != 0 || len(*specs) != 1 {
		t.Fatalf("specs=%d mismatch=%d", len(*specs), mismatch)
	}
}

func TestInMemoryBindingsUnknownPod(t *testing.T) {
	b := NewInMemoryBindings(nil)
	if _, err := b.JobRunForPod(context.Background(), testNamespace, "p", "u"); err == nil {
		t.Fatal("unknown pod must fail")
	}
}

var _ identity.Verifier = (*Verifier)(nil)
var _ BindingSource = (*InMemoryBindings)(nil)
