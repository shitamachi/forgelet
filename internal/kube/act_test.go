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
	if pod.Spec.InitContainers[0].Image != ref || !strings.Contains(pod.Spec.InitContainers[0].StartupProbe.Exec.Command[2], "28.5.1") {
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
	if len(pod.Spec.InitContainers) != 1 {
		t.Fatal("missing native engine")
	}
	engine := pod.Spec.InitContainers[0]
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
	for _, e := range pod.Spec.InitContainers[0].Env {
		if strings.Contains(e.Name, "CACHE") || strings.Contains(e.Name, "CONFIG") {
			t.Fatal("job capability/config entered engine init", e.Name)
		}
	}
}
