package policy

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/security/identity"
)

func ident(job model.JobRunID, scopes ...string) identity.Identity {
	return identity.Identity{
		Audience:  identity.Audience,
		Namespace: "forgelet-jobs",
		PodUID:    "pod-1",
		JobRunID:  job,
		Scopes:    scopes,
		IssuedAt:  time.Unix(1_700_000_000, 0).UTC(),
		ExpiresAt: time.Unix(1_700_000_000, 0).UTC().Add(time.Minute),
	}
}

func TestAuthorizeExecution(t *testing.T) {
	job := model.JobRunID("01JTEST0000000000000000001")
	if err := AuthorizeExecution(ident(job), job); err != nil {
		t.Fatalf("matching binding rejected: %v", err)
	}
	err := AuthorizeExecution(ident(model.JobRunID("01JOTHER00000000000000000X")), job)
	if !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("mismatched binding accepted: %v", err)
	}
	// The error names job runs, never secret values.
	if !strings.Contains(err.Error(), string(job)) {
		t.Errorf("error lacks requested job id: %v", err)
	}
}

func TestDecideSecretsScopeGate(t *testing.T) {
	job := model.JobRunID("01JTEST0000000000000000002")
	id := ident(job, identity.ScopePlanRead) // no secrets:read
	req := []Ref{{Scope: "repository", Name: "TOKEN"}}
	d := DecideSecrets(id, req, req, TrustTrusted)
	if len(d.Allowed) != 0 || len(d.Denied) != 1 {
		t.Fatalf("scope-less identity got %+v", d)
	}
	if !strings.Contains(d.Denied[0].Reason, identity.ScopeSecretsRead) {
		t.Errorf("denial reason lacks scope: %+v", d.Denied[0])
	}
}

func TestDecideSecretsForkDeniesAll(t *testing.T) {
	job := model.JobRunID("01JTEST0000000000000000003")
	id := ident(job, identity.ScopeSecretsRead)
	refs := []Ref{{Scope: "repository", Name: "TOKEN"}, {Scope: "environment", Name: "PROD"}}
	d := DecideSecrets(id, refs, refs, TrustFork)
	if len(d.Allowed) != 0 || len(d.Denied) != 2 {
		t.Fatalf("fork run received secrets: %+v", d)
	}
	for _, denied := range d.Denied {
		if !strings.Contains(denied.Reason, "fork") {
			t.Errorf("unexpected fork denial reason: %q", denied.Reason)
		}
	}
}

func TestDecideSecretsPlanIntersection(t *testing.T) {
	job := model.JobRunID("01JTEST0000000000000000004")
	id := ident(job, identity.ScopeSecretsRead)
	planRefs := []Ref{
		{Scope: "repository", Name: "REGISTRY_TOKEN"},
		{Scope: "environment", Name: "DEPLOY_KEY"},
	}
	requested := []Ref{
		planRefs[0],
		{Scope: "repository", Name: "UNREFERENCED"}, // not in plan
		{Scope: "organization", Name: "DEPLOY_KEY"}, // wrong scope, same name
	}
	d := DecideSecrets(id, requested, planRefs, TrustSameRepo)
	if len(d.Allowed) != 1 || d.Allowed[0] != planRefs[0] {
		t.Fatalf("allowed = %+v, want only REGISTRY_TOKEN", d.Allowed)
	}
	if len(d.Denied) != 2 {
		t.Fatalf("denied = %+v, want 2", d.Denied)
	}
	for _, denied := range d.Denied {
		if !strings.Contains(denied.Reason, "plan") {
			t.Errorf("denial reason %q does not explain plan scoping", denied.Reason)
		}
	}
}

// TestDecisionsNeverCarryValues documents the FR-S3.4 invariant where it is
// decided: reasons and refs only ever contain scope/name strings.
func TestDecideSecretsNeverCarryValues(t *testing.T) {
	job := model.JobRunID("01JTEST0000000000000000005")
	id := ident(job, identity.ScopeSecretsRead)
	planRefs := []Ref{{Scope: "repository", Name: "TOKEN"}}
	d := DecideSecrets(id, []Ref{{Scope: "repository", Name: "TOKEN"}, {Scope: "repository", Name: "X"}}, planRefs, TrustTrusted)
	blob := d.Denied[0].Reason
	for _, ref := range append(d.Allowed, planRefs...) {
		blob += ref.Scope + ref.Name
	}
	// Values never enter the package, so a representative plaintext can not
	// appear in any decision artifact.
	plaintext := "ghp_sup3rsecretvalue"
	if strings.Contains(blob, plaintext) {
		t.Fatal("decision artifacts contain secret material")
	}
}
