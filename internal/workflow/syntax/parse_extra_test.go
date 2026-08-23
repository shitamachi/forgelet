package syntax

import (
	"strings"
	"testing"
)

func TestParseScalarValueTypes(t *testing.T) {
	// runs-on as a boolean / number must be rejected with a location.
	src := "on: push\njobs:\n  a:\n    runs-on: 42\n    steps:\n      - run: x\n"
	_, err := Parse("wf.yml", []byte(src))
	if err == nil || !strings.Contains(err.Error(), "expected string") {
		t.Fatalf("numeric runs-on: %v", err)
	}

	// branches as a mapping instead of a sequence.
	src = "on:\n  push:\n    branches:\n      main: true\njobs:\n  a:\n    runs-on: x\n    steps:\n      - run: x\n"
	_, err = Parse("wf.yml", []byte(src))
	if err == nil || !strings.Contains(err.Error(), "expected sequence") {
		t.Fatalf("branches mapping: %v", err)
	}

	// step env as a sequence.
	src = "on: push\njobs:\n  a:\n    runs-on: x\n    env:\n      - A\n    steps:\n      - run: x\n"
	_, err = Parse("wf.yml", []byte(src))
	if err == nil || !strings.Contains(err.Error(), "expected mapping of env") {
		t.Fatalf("env sequence: %v", err)
	}

	// on.push as a scalar that isn't a trigger name.
	src = "on:\n  push: deploy\njobs:\n  a:\n    runs-on: x\n    steps:\n      - run: x\n"
	_, err = Parse("wf.yml", []byte(src))
	if err == nil || !strings.Contains(err.Error(), "expected mapping of push filters") {
		t.Fatalf("push scalar value: %v", err)
	}
}

func TestParsePushNullFilters(t *testing.T) {
	src := "on:\n  push:\njobs:\n  a:\n    runs-on: x\n    steps:\n      - run: x\n"
	wf, err := Parse("wf.yml", []byte(src))
	if err != nil {
		t.Fatalf("empty push mapping: %v", err)
	}
	if wf.On.Push == nil {
		t.Fatal("push trigger missing for empty mapping")
	}
}

func TestParseStepMappingRequired(t *testing.T) {
	src := "on: push\njobs:\n  a:\n    runs-on: x\n    steps:\n      - just_a_string\n"
	_, err := Parse("wf.yml", []byte(src))
	if err == nil || !strings.Contains(err.Error(), "expected mapping") {
		t.Fatalf("scalar step: %v", err)
	}
}

func TestIsIdentifier(t *testing.T) {
	valid := []string{"a", "A_b", "job-1", "_x", "Build9"}
	invalid := []string{"", "1abc", "-lead", "has space", "dot.name", "中"}
	for _, s := range valid {
		if !isIdentifier(s) {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range invalid {
		if isIdentifier(s) {
			t.Errorf("%q should be invalid", s)
		}
	}
}
