package kube

import (
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestActMirrorsAreAdministratorConfigurationOnly(t *testing.T) {
	rt, client, job, profile := fixture(t)
	ref := "zot.example/engine@sha256:" + strings.Repeat("a", 64)
	profile.Act = &ActProfile{RunnerImage: ref, EngineImage: ref, RegistrySecret: "zot-registry", ImageMirrors: map[string]string{"postgres:18-alpine": ref}}
	if err := profile.Validate(); err != nil {
		t.Fatal(err)
	}
	job.Plan.Kernel = "act"
	if _, err := rt.Ensure(t.Context(), job, profile); err != nil {
		t.Fatal(err)
	}
	pod, err := client.CoreV1().Pods(rt.Namespace).Get(t.Context(), job.PodName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if pod.Spec.InitContainers[1].Image != ref || !strings.Contains(pod.Spec.InitContainers[1].StartupProbe.Exec.Command[2], "28.5.1") {
		t.Fatal("engine mirror lost pinned reference or version check")
	}
	found := false
	for _, e := range pod.Spec.Containers[0].Env {
		if e.Name == "FORGELET_IMAGE_MIRRORS" {
			var images map[string]string
			if json.Unmarshal([]byte(e.Value), &images) != nil || images["postgres:18-alpine"] != ref {
				t.Fatal("mirror catalog not passed")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("missing mirror catalog")
	}
	profile.Act.ImageMirrors["postgres:18-alpine"] = "other.example/engine@sha256:" + strings.Repeat("a", 64)
	if profile.Validate() == nil {
		t.Fatal("accepted unrelated credential host")
	}
	profile.Act.EngineImage = "docker:unknown"
	if profile.Act.Validate() == nil {
		t.Fatal("accepted mutable unverified engine version")
	}
}

func TestExplicitActEngineNeverGetsWorkloadIdentity(t *testing.T) {
	rt, client, job, profile := fixture(t)
	profile.Act = &ActProfile{RegistrySecret: "registry"}
	job.Plan.Kernel = "act"
	if _, err := rt.Ensure(t.Context(), job, profile); err != nil {
		t.Fatal(err)
	}
	pod, err := client.CoreV1().Pods(rt.Namespace).Get(t.Context(), job.PodName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pod.Spec.InitContainers) != 2 {
		t.Fatal("missing native engine")
	}
	engine := pod.Spec.InitContainers[1]
	if engine.RestartPolicy == nil || !*engine.SecurityContext.Privileged {
		t.Fatal("engine profile was not explicit")
	}
	for _, mount := range engine.VolumeMounts {
		if mount.Name == "identity" || mount.Name == "act-registry" {
			t.Fatal("control identity/registry config leaked into engine")
		}
	}
	for _, volume := range pod.Spec.Volumes {
		if volume.HostPath != nil {
			t.Fatal("host mount")
		}
	}
	if pod.Spec.HostNetwork || *pod.Spec.AutomountServiceAccountToken {
		t.Fatal("unexpected host/API access")
	}
	if got := pod.Spec.Containers[0].Env; len(got) == 0 {
		t.Fatal("missing supervisor config")
	}
	rt, _, job, profile = fixture(t)
	job.Plan.Kernel = "act"
	if _, err := rt.Ensure(t.Context(), job, profile); err == nil {
		t.Fatal("act ran without opt-in profile")
	}
}

func TestBuildCacheAddressAndBuildkitDefaultsOnlyReachSupervisor(t *testing.T) {
	rt, client, job, profile := fixture(t)
	profile.Act = &ActProfile{BuildkitConfig: "[registry.\"example.test\"]\n  http = true\n"}
	job.Plan.Kernel = "act"
	if _, err := rt.Ensure(t.Context(), job, profile); err != nil {
		t.Fatal(err)
	}
	pod, err := client.CoreV1().Pods(rt.Namespace).Get(t.Context(), job.PodName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	host, config := false, false
	for _, e := range pod.Spec.Containers[0].Env {
		if e.Name == "FORGELET_ACT_CACHE_HOST" {
			host = e.ValueFrom != nil && e.ValueFrom.FieldRef.FieldPath == "status.podIP"
		}
		if e.Name == "FORGELET_BUILDKIT_CONFIG" {
			config = e.Value == profile.Act.BuildkitConfig
		}
	}
	if !host || !config {
		t.Fatal("missing bounded job cache or builder defaults")
	}
	for _, e := range pod.Spec.InitContainers[1].Env {
		if strings.Contains(e.Name, "CACHE") || strings.Contains(e.Name, "CONFIG") {
			t.Fatal("job capability/config entered engine init", e.Name)
		}
	}
}

func TestActEngineResourceAdmission(t *testing.T) {
	for _, tc := range []struct {
		name    string
		act     ActProfile
		invalid bool
		cpus    string
	}{
		{name: "defaults", cpus: "3"},
		{name: "bounded worker", act: ActProfile{CPURequest: "1", CPULimit: "1", MemoryRequest: "4Gi", MemoryLimit: "4Gi"}, cpus: "1"},
		{name: "sub CPU budget", act: ActProfile{CPULimit: "250m", MemoryLimit: "2Gi"}, cpus: "1"},
		{name: "fractional CPU budget", act: ActProfile{CPULimit: "1500m", MemoryLimit: "2Gi"}, cpus: "2"},
		{name: "invalid quantity", act: ActProfile{CPURequest: "invalid"}, invalid: true},
		{name: "negative", act: ActProfile{MemoryLimit: "-1Gi"}, invalid: true},
		{name: "zero", act: ActProfile{CPURequest: "0"}, invalid: true},
		{name: "limit below default request", act: ActProfile{CPULimit: "50m"}, invalid: true},
		{name: "request exceeds default", act: ActProfile{CPURequest: "4"}, invalid: true},
		{name: "memory exceeds limit", act: ActProfile{MemoryRequest: "4Gi", MemoryLimit: "2Gi"}, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, client, job, profile := fixture(t)
			profile.Act = &tc.act
			profile.NodeSelector = map[string]string{"forgelet.dev/execution": "true"}
			job.Plan.Kernel = "act"
			_, err := rt.Ensure(t.Context(), job, profile)
			if tc.invalid {
				if err == nil {
					t.Fatal("accepted invalid resources")
				}
				if len(client.Actions()) != 0 {
					t.Fatal("invalid resources reached Kubernetes")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			pod, err := client.CoreV1().Pods(rt.Namespace).Get(t.Context(), job.PodName, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if pod.Spec.NodeSelector["forgelet.dev/execution"] != "true" {
				t.Fatal("lost execution node restriction")
			}
			r := pod.Spec.InitContainers[1].Resources
			cpu, mem := "100m", "256Mi"
			if tc.act.CPURequest != "" {
				cpu = tc.act.CPURequest
				mem = tc.act.MemoryRequest
			}
			if r.Requests.Cpu().String() != cpu || r.Requests.Memory().String() != mem {
				t.Fatalf("wrong engine reservation: %v", r)
			}
			if tc.act.CPULimit != "" && (r.Limits.Cpu().String() != tc.act.CPULimit || r.Limits.Memory().String() != tc.act.MemoryLimit) {
				t.Fatalf("wrong engine limits: %v", r)
			}
			var cpus string
			for _, env := range pod.Spec.Containers[0].Env {
				if env.Name == "FORGELET_ACT_CPU_COUNT" {
					cpus = env.Value
				}
			}
			if cpus != tc.cpus {
				t.Fatalf("job logical CPUs = %q, want %q", cpus, tc.cpus)
			}
		})
	}
}

func TestActTemporaryPathsShareOnlyTheBoundedEngineVolume(t *testing.T) {
	rt, client, job, profile := fixture(t)
	profile.Act = &ActProfile{DataLimit: "8Gi", InsecureRegistry: true, RunnerImage: "registry.test/runner@sha256:" + strings.Repeat("a", 64)}
	job.Plan.Kernel = "act"
	if _, err := rt.Ensure(t.Context(), job, profile); err != nil {
		t.Fatal(err)
	}
	pod, err := client.CoreV1().Pods(rt.Namespace).Get(t.Context(), job.PodName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	engine := pod.Spec.InitContainers[1]
	if engine.Command[0] != "/run/forgelet-act/engine-bootstrap" || engine.Command[1] != "act-engine" || engine.Command[len(engine.Command)-1] != "--insecure-registry=registry.test" {
		t.Fatal("engine arguments or tmp permissions lost", engine.Command)
	}
	found := false
	for _, mount := range engine.VolumeMounts {
		if mount.MountPath == "/tmp" {
			found = mount.Name == "act-data" && mount.SubPath == "forgelet-tmp"
		}
	}
	if !found {
		t.Fatal("missing same-attempt bounded temporary volume")
	}
	for _, mount := range pod.Spec.Containers[0].VolumeMounts {
		if mount.MountPath == "/tmp" {
			t.Fatal("supervisor metadata entered job temp namespace")
		}
	}
	for _, volume := range pod.Spec.Volumes {
		if volume.Name == "act-data" && volume.EmptyDir.SizeLimit.String() != "8Gi" {
			t.Fatal("lost shared data budget")
		}
		if volume.Name == "act-tmp" || volume.HostPath != nil {
			t.Fatal("unbounded or host temporary storage")
		}
	}
}
