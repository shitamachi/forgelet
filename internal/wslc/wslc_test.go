package wslc

import (
	"slices"
	"strings"
	"testing"
)

func TestNameIsLowerCaseAndBounded(t *testing.T) {
	name := Name("RUN-01", "job_ABCDEF123456", "frja")
	if name != "forgelet-run01-jobabcde-frja" {
		t.Fatalf("unexpected name %q", name)
	}
	if got := Name("", "", ""); got != "forgelet-attempt-attempt-attempt" {
		t.Fatalf("unexpected fallback name %q", got)
	}
}

func TestRunArgsCarryOnlyCapabilities(t *testing.T) {
	config := Config{
		Image:  "registry.example/forgelet/runner@sha256:abc",
		CPUs:   2.5,
		Memory: "4G",
		DNS:    []string{"10.0.0.53", "10.0.0.54"},
		User:   "1000:1000",
	}
	args := RunArgs(config, "forgelet-run-job-attempt", "job", "attempt")
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"run --interactive",
		"--name forgelet-run-job-attempt",
		"--label forgelet.owner=forgelet-runner",
		"--label forgelet.job=job",
		"--label forgelet.attempt=attempt",
		"--pull missing",
		"--cpus 2.5",
		"--memory 4G",
		"--dns 10.0.0.53",
		"--dns 10.0.0.54",
		"--user 1000:1000",
		"registry.example/forgelet/runner@sha256:abc /usr/local/bin/forgelet-runner --exec",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("run arguments %q do not contain %q", joined, want)
		}
	}
	if slices.Contains(args, "--rm") {
		t.Fatal("a job container must stay inspectable until the runner removes it explicitly")
	}
}

func TestRunArgsOmitUnsetLimits(t *testing.T) {
	args := RunArgs(Config{Image: "runner:1"}, "forgelet-run-job-attempt", "job", "attempt")
	joined := strings.Join(args, " ")
	for _, unwanted := range []string{"--cpus", "--memory", "--dns", "--user"} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("unexpected %s in %q", unwanted, joined)
		}
	}
}

func TestParseListReadsOwnedContainers(t *testing.T) {
	names, err := ParseList([]byte(`[{"ID":"c8c7","Names":"forgelet-a-b-c","State":"exited"},{"ID":"d1","Names":"forgelet-d-e-f"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(names, []string{"forgelet-a-b-c", "forgelet-d-e-f"}) {
		t.Fatalf("unexpected names %v", names)
	}
	if names, err := ParseList([]byte(`[]`)); err != nil || len(names) != 0 {
		t.Fatalf("empty list: %v %v", names, err)
	}
	if names, err := ParseList([]byte("  \n")); err != nil || len(names) != 0 {
		t.Fatalf("no matches: %v %v", names, err)
	}
	// One match is printed as a bare object, not a one-element array.
	single, err := ParseList([]byte(`{"ID":"c8c7","Names":"forgelet-only","State":"exited"}`))
	if err != nil || !slices.Equal(single, []string{"forgelet-only"}) {
		t.Fatalf("single container: %v %v", single, err)
	}
}

func TestParseExitCode(t *testing.T) {
	code, err := ParseExitCode([]byte(`[{"State":{"ExitCode":7,"Running":false}}]`))
	if err != nil || code != 7 {
		t.Fatalf("exit code %d, %v", code, err)
	}
	if _, err := ParseExitCode([]byte(`[{"State":{"ExitCode":0,"Running":true}}]`)); err == nil {
		t.Fatal("a running container has no exit status yet")
	}
	if _, err := ParseExitCode([]byte(`[]`)); err == nil {
		t.Fatal("a missing container has no exit status")
	}
	if _, err := ParseExitCode([]byte(`not json`)); err == nil {
		t.Fatal("invalid JSON must fail")
	}
}

func TestValidate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config Config
		ok     bool
	}{
		{"image", Config{Image: "runner@sha256:abc"}, true},
		{"missing image", Config{}, false},
		{"spaced image", Config{Image: "a b"}, false},
		{"negative cpus", Config{Image: "a", CPUs: -1}, false},
		{"relative ca", Config{Image: "a", CAFile: "ca.pem"}, false},
		{"absolute ca", Config{Image: "a", CAFile: `C:\runner\ca.pem`}, true},
		{"posix ca", Config{Image: "a", CAFile: "/runner/ca.pem"}, true},
		{"unc ca", Config{Image: "a", CAFile: `\\server\share\ca.pem`}, true},
		{"drive without separator", Config{Image: "a", CAFile: `C:ca.pem`}, false},
		{"bad dns", Config{Image: "a", DNS: []string{"a b"}}, false},
		{"spaced memory", Config{Image: "a", Memory: "4 G"}, false},
		{"control url", Config{Image: "a", ControlURL: "https://forgelet.local"}, true},
		{"control url without host", Config{Image: "a", ControlURL: "https://"}, false},
		{"control url credentials", Config{Image: "a", ControlURL: "https://u:p@forgelet.local"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.config.Validate()
			if (err == nil) != tc.ok {
				t.Fatalf("Validate() = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}
