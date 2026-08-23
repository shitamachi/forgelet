package plan

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shitamachi/forgelet/internal/run/model"
)

func basePlan() *Plan {
	return &Plan{
		JobRunID:    model.JobRunID("01JTEST0000000000000000000"),
		Repository:  model.RepositoryRef{Provider: "github", Owner: "shitamachi", Name: "forgelet"},
		SHA:         "abc123",
		Ref:         "refs/heads/main",
		RunnerClass: "k3s-small",
		Env:         map[string]string{"GO": "1.27", "OS": "linux"},
		Steps: []Step{
			{ID: "checkout", Run: RunStep{Script: "true"}},
			{ID: "test", Run: RunStep{Script: "go test ./...", Env: map[string]string{"VERBOSE": "1"}}},
		},
		SecretRefs: []SecretRef{
			{Scope: "repository", Name: "REGISTRY_TOKEN", Env: "REGISTRY_TOKEN"},
		},
		TimeoutSecs: 600,
	}
}

func TestDigestStableAcrossMapOrder(t *testing.T) {
	first := basePlan()
	firstDigest, err := first.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	// Rebuild with reversed insertion orders for every map; Go map iteration
	// order is randomized anyway, this makes the intent explicit.
	second := basePlan()
	second.Env = map[string]string{"OS": "linux", "GO": "1.27"}
	second.Steps[1].Run.Env = map[string]string{"VERBOSE": "1"}
	secondDigest, err := second.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if firstDigest != secondDigest {
		t.Errorf("same plan digested differently: %s vs %s", firstDigest, secondDigest)
	}
	if len(firstDigest) != 64 || !isHex(firstDigest) {
		t.Errorf("digest %q is not sha256 hex", firstDigest)
	}
}

func TestDigestChangesWithContent(t *testing.T) {
	first, err := basePlan().Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}

	withStep := basePlan()
	withStep.Steps = append(withStep.Steps, Step{ID: "build", Run: RunStep{Script: "go build ./..."}})
	if d, _ := withStep.Digest(); d == first {
		t.Error("adding a step must change the digest")
	}

	withSecret := basePlan()
	withSecret.SecretRefs = append(withSecret.SecretRefs, SecretRef{Scope: "environment", Name: "PROD_TOKEN"})
	if d, _ := withSecret.Digest(); d == first {
		t.Error("adding a secret reference must change the digest")
	}

	withTimeout := basePlan()
	withTimeout.TimeoutSecs = 601
	if d, _ := withTimeout.Digest(); d == first {
		t.Error("changing timeout must change the digest")
	}
}

// TestSecretRefCarriesNoValue guards the spec 0002 invariant FR-F.1 from the
// serialization side: a marshalled secret reference exposes only reference
// fields, never a resolved value.
func TestSecretRefCarriesNoValue(t *testing.T) {
	b, err := json.Marshal(SecretRef{Scope: "repository", Name: "TOKEN", Env: "TOKEN"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := map[string]bool{"scope": true, "name": true, "env": true}
	if len(m) != len(want) {
		t.Errorf("SecretRef serialized unexpected fields: %s", b)
	}
	for key := range m {
		if !want[key] {
			t.Errorf("SecretRef serialized unexpected field %q: %s", key, b)
		}
	}
}

func TestDigestErrors(t *testing.T) {
	var nilPlan *Plan
	if _, err := nilPlan.Digest(); err == nil {
		t.Error("nil plan must error")
	}
	noID := basePlan()
	noID.JobRunID = ""
	if _, err := noID.Digest(); err == nil {
		t.Error("empty job run id must error")
	}
	noSteps := basePlan()
	noSteps.Steps = nil
	if _, err := noSteps.Digest(); err == nil {
		t.Error("plan without steps must error")
	}
}

func isHex(s string) bool {
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}
