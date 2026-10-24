package kube

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestBuildSidecarIsRootlessScopedAndDemandOnly(t *testing.T) {
	rt, client, job, profile := fixture(t)
	profile.Build = &BuildProfile{Images: []string{"ghcr.io/test/image"}, RegistrySecret: "registry-auth"}
	if _, err := rt.Ensure(t.Context(), job, profile); err != nil {
		t.Fatal(err)
	}
	p, err := client.CoreV1().Pods("ci").Get(t.Context(), job.PodName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Spec.InitContainers) != 0 {
		t.Fatal("builder allocated for shell job")
	}
	job.ID = "build"
	job.PodName = "f-build"
	job.Plan.Steps = []run.Step{{Action: &run.Action{Uses: "forgelet/build@v1"}}}
	if _, err := rt.Ensure(t.Context(), job, profile); err != nil {
		t.Fatal(err)
	}
	p, err = client.CoreV1().Pods("ci").Get(t.Context(), job.PodName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Spec.InitContainers) != 1 || p.Spec.InitContainers[0].RestartPolicy == nil || *p.Spec.InitContainers[0].RestartPolicy != corev1.ContainerRestartPolicyAlways {
		t.Fatal("missing native builder sidecar")
	}
	if *p.Spec.SecurityContext.RunAsUser != 1000 || *p.Spec.Containers[0].SecurityContext.AllowPrivilegeEscalation {
		t.Fatal("executor privileges changed")
	}
	b := p.Spec.InitContainers[0]
	if b.SecurityContext.Privileged != nil && *b.SecurityContext.Privileged {
		t.Fatal("privileged builder")
	}
	for _, v := range p.Spec.Volumes {
		if v.HostPath != nil {
			t.Fatal("hostPath")
		}
	}
	for _, mount := range b.VolumeMounts {
		if mount.Name == "identity" || mount.Name == "registry-auth" {
			t.Fatal("builder received unnecessary credentials")
		}
	}
	job.ID = "denied"
	job.PodName = "f-denied"
	profile.Build = nil
	if _, err := rt.Ensure(t.Context(), job, profile); err == nil {
		t.Fatal("build accepted on ordinary profile")
	}
}
