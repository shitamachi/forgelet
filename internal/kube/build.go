package kube

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/shitamachi/forgelet/internal/run"
)

const BuildkitImage = "moby/buildkit:v0.20.2-rootless"

type BuildProfile struct {
	Images         []string `yaml:"images" json:"images"`
	RegistrySecret string   `yaml:"registry_secret" json:"registry_secret"`
	// InsecureRegistry is only for a registry explicitly configured by the
	// administrator (e.g. a disposable test registry), never a workflow input.
	InsecureRegistry bool   `yaml:"insecure_registry" json:"insecure_registry"`
	CPURequest       string `yaml:"cpu_request" json:"cpu_request"`
	MemoryRequest    string `yaml:"memory_request" json:"memory_request"`
	CPULimit         string `yaml:"cpu_limit" json:"cpu_limit"`
	MemoryLimit      string `yaml:"memory_limit" json:"memory_limit"`
}

func (b BuildProfile) Validate() error {
	if len(b.Images) == 0 || len(b.Images) > 32 {
		return fmt.Errorf("build profile needs 1..32 allowed image repositories")
	}
	for _, image := range b.Images {
		if !run.ValidImage(image) {
			return fmt.Errorf("build image must be a registry-qualified repository without tag/digest")
		}
	}
	return (Profile{Image: BuildkitImage, CPURequest: b.CPURequest, MemoryRequest: b.MemoryRequest, CPULimit: b.CPULimit, MemoryLimit: b.MemoryLimit}).Validate()
}

func addBuilder(pod *corev1.Pod, b BuildProfile) {
	yes := true
	restart := corev1.ContainerRestartPolicyAlways
	resources := corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("256Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("2Gi")}}
	for name, value := range map[corev1.ResourceName]string{corev1.ResourceCPU: b.CPURequest, corev1.ResourceMemory: b.MemoryRequest} {
		if value != "" {
			resources.Requests[name] = resource.MustParse(value)
		}
	}
	for name, value := range map[corev1.ResourceName]string{corev1.ResourceCPU: b.CPULimit, corev1.ResourceMemory: b.MemoryLimit} {
		if value != "" {
			resources.Limits[name] = resource.MustParse(value)
		}
	}
	// Kubernetes native sidecars stop with the executor, including cancellation
	// and process failure; they do not keep a completed Pod running indefinitely.
	pod.Spec.InitContainers = append(pod.Spec.InitContainers, corev1.Container{Name: "buildkit", Image: BuildkitImage, RestartPolicy: &restart, Args: []string{"--addr=unix:///run/forgelet-build/buildkitd.sock", "--oci-worker-no-process-sandbox", "--oci-worker-snapshotter=native"}, Resources: resources,
		SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &yes, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}, Add: []corev1.Capability{"SETUID", "SETGID"}}, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeUnconfined}, AppArmorProfile: &corev1.AppArmorProfile{Type: corev1.AppArmorProfileTypeUnconfined}},
		StartupProbe:    &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"buildctl", "--addr=unix:///run/forgelet-build/buildkitd.sock", "debug", "workers"}}}, PeriodSeconds: 2, FailureThreshold: 60},
		VolumeMounts:    []corev1.VolumeMount{{Name: "build-socket", MountPath: "/run/forgelet-build"}, {Name: "build-data", MountPath: "/home/user/.local/share/buildkit"}},
	})
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{Name: "build-socket", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}, corev1.Volume{Name: "build-data", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: quantityPointer("8Gi")}}})
	main := &pod.Spec.Containers[0]
	main.VolumeMounts = append(main.VolumeMounts, corev1.VolumeMount{Name: "build-socket", MountPath: "/run/forgelet-build"})
	main.Env = append(main.Env, corev1.EnvVar{Name: "FORGELET_BUILD_ADDR", Value: "unix:///run/forgelet-build/buildkitd.sock"}, corev1.EnvVar{Name: "FORGELET_BUILD_IMAGES", Value: strings.Join(b.Images, "\n")})
	if b.InsecureRegistry {
		main.Env = append(main.Env, corev1.EnvVar{Name: "FORGELET_BUILD_INSECURE", Value: "true"})
	}
	if b.RegistrySecret != "" {
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{Name: "registry-auth", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: b.RegistrySecret, Items: []corev1.KeyToPath{{Key: ".dockerconfigjson", Path: "config.json"}}}}})
		main.VolumeMounts = append(main.VolumeMounts, corev1.VolumeMount{Name: "registry-auth", MountPath: "/var/run/forgelet-registry", ReadOnly: true})
		main.Env = append(main.Env, corev1.EnvVar{Name: "FORGELET_REGISTRY_CONFIG", Value: "/var/run/forgelet-registry"})
	}
}
