package kube

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/shitamachi/forgelet/internal/blob"
	"github.com/shitamachi/forgelet/internal/run"
)

// ActProfile explicitly opts trusted jobs into a Pod-owned privileged engine.
// The registry Secret is mounted only into the supervising executor.
type ActProfile struct {
	CacheObjectBytes int64             `yaml:"cache_object_bytes,omitempty" json:"cache_object_bytes,omitempty"`
	CPURequest       string            `yaml:"cpu_request,omitempty" json:"cpu_request,omitempty"`
	MemoryRequest    string            `yaml:"memory_request,omitempty" json:"memory_request,omitempty"`
	CPULimit         string            `yaml:"cpu_limit,omitempty" json:"cpu_limit,omitempty"`
	MemoryLimit      string            `yaml:"memory_limit,omitempty" json:"memory_limit,omitempty"`
	BuildkitConfig   string            `yaml:"buildkit_config,omitempty" json:"buildkit_config,omitempty"`
	ImageMirrors     map[string]string `yaml:"image_mirrors,omitempty" json:"image_mirrors,omitempty"`
	RunnerImage      string            `yaml:"runner_image,omitempty" json:"runner_image,omitempty"`
	InsecureRegistry bool              `yaml:"insecure_registry,omitempty" json:"insecure_registry,omitempty"`
	EngineImage      string            `yaml:"engine_image" json:"engine_image"`
	RegistrySecret   string            `yaml:"registry_secret" json:"registry_secret"`
	DataLimit        string            `yaml:"data_limit" json:"data_limit"`
}

const ActEngineImage = "docker:28.5.1-dind"

func (a ActProfile) Validate() error {
	if a.CacheObjectBytes != 0 && (a.CacheObjectBytes < 2048 || a.CacheObjectBytes > blob.MaxCacheArchive) {
		return fmt.Errorf("act cache_object_bytes must be 2048..1GiB (0 uses 256MiB)")
	}
	if err := (Profile{Image: ActEngineImage, CPURequest: a.CPURequest, MemoryRequest: a.MemoryRequest, CPULimit: a.CPULimit, MemoryLimit: a.MemoryLimit}).Validate(); err != nil {
		return fmt.Errorf("act engine resources: %w", err)
	}
	resources := a.resources()
	for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		request, limit := resources.Requests[name], resources.Limits[name]
		if request.Cmp(limit) > 0 {
			return fmt.Errorf("act engine %s request exceeds limit", name)
		}
	}

	if len(a.BuildkitConfig) > 64<<10 || strings.ContainsRune(a.BuildkitConfig, 0) {
		return fmt.Errorf("invalid act buildkit_config (maximum 64 KiB)")
	}
	if a.EngineImage != "" && a.EngineImage != ActEngineImage && !run.PinnedImage(a.EngineImage) {
		return fmt.Errorf("act engine image must be %s or its SHA256-pinned mirror", ActEngineImage)
	}
	if a.DataLimit != "" {
		q, err := resource.ParseQuantity(a.DataLimit)
		if err != nil || q.Sign() <= 0 || q.Cmp(resource.MustParse("64Gi")) > 0 {
			return fmt.Errorf("invalid act data_limit (maximum 64Gi)")
		}
	}
	if strings.ContainsAny(a.RegistrySecret, "/\\\n") {
		return fmt.Errorf("invalid act registry secret")
	}
	return nil
}
func addAct(pod *corev1.Pod, a ActProfile, image string) {
	if a.RunnerImage != "" {
		image = a.RunnerImage
	}
	yes, no := true, false
	root := int64(0)
	restart := corev1.ContainerRestartPolicyAlways
	limit := a.DataLimit
	if limit == "" {
		limit = "12Gi"
	}
	socket := corev1.VolumeMount{Name: "act-socket", MountPath: "/run/forgelet-act"}
	engine := a.EngineImage
	if engine == "" {
		engine = ActEngineImage
	}
	pod.Spec.InitContainers = append(pod.Spec.InitContainers, corev1.Container{Name: "act-bootstrap", Image: pod.Spec.Containers[0].Image,
		Command:         []string{"sh", "-ec", "cp /usr/local/bin/forgelet-executor /run/forgelet-act/engine-bootstrap; chmod 0555 /run/forgelet-act/engine-bootstrap"},
		Resources:       pod.Spec.Containers[0].Resources,
		SecurityContext: pod.Spec.Containers[0].SecurityContext,
		VolumeMounts:    []corev1.VolumeMount{socket},
	}, corev1.Container{Name: "act-engine", Image: engine, RestartPolicy: &restart,
		Command:         []string{"/run/forgelet-act/engine-bootstrap", "act-engine", "--host=unix://" + run.ActSocket, "--group=1000", "--storage-driver=overlay2", "--exec-opt=native.cgroupdriver=cgroupfs"},
		SecurityContext: &corev1.SecurityContext{Privileged: &yes, RunAsNonRoot: &no, RunAsUser: &root, RunAsGroup: &root, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeUnconfined}},
		Resources:       a.resources(),
		StartupProbe:    &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"sh", "-ec", `test "$(docker --host=unix://` + run.ActSocket + ` info --format '{{.ServerVersion}}')" = 28.5.1`}}}, PeriodSeconds: 2, FailureThreshold: 60},
		VolumeMounts:    []corev1.VolumeMount{socket, {Name: "act-data", MountPath: "/var/lib/docker"}, {Name: "act-data", SubPath: "forgelet-tmp", MountPath: "/tmp"}, {Name: "workspace", MountPath: "/workspace"}},
	})
	if a.InsecureRegistry {
		host, _, ok := strings.Cut(image, "/")
		if ok {
			last := len(pod.Spec.InitContainers) - 1
			pod.Spec.InitContainers[last].Command = append(pod.Spec.InitContainers[last].Command, "--insecure-registry="+host)
		}
	}
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{Name: "act-socket", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}, corev1.Volume{Name: "act-data", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: quantityPointer(limit)}}})
	main := &pod.Spec.Containers[0]
	// A nested private cgroup hides the engine's quota from toolchain discovery.
	// Advertise its rounded-up logical CPU count inside the job as well. The
	// engine ancestor still enforces the exact fractional aggregate budget.
	resources := a.resources()
	cpu := resources.Limits.Cpu().Value()
	main.Env = append(main.Env, corev1.EnvVar{Name: "FORGELET_ACT_CPU_COUNT", Value: strconv.FormatInt(cpu, 10)})
	main.VolumeMounts = append(main.VolumeMounts, socket)
	main.Env = append(main.Env, corev1.EnvVar{Name: "FORGELET_ACT_CACHE_HOST", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.podIP"}}}, corev1.EnvVar{Name: "DOCKER_HOST", Value: "unix://" + run.ActSocket}, corev1.EnvVar{Name: "FORGELET_ACT_IMAGE", Value: image})
	if a.CacheObjectBytes > 0 {
		main.Env = append(main.Env, corev1.EnvVar{Name: "FORGELET_ACT_CACHE_OBJECT_BYTES", Value: strconv.FormatInt(a.CacheObjectBytes, 10)})
	}
	if a.BuildkitConfig != "" {
		main.Env = append(main.Env, corev1.EnvVar{Name: "FORGELET_BUILDKIT_CONFIG", Value: a.BuildkitConfig})
	}
	if len(a.ImageMirrors) > 0 {
		encoded, _ := json.Marshal(a.ImageMirrors) // A string map cannot fail JSON encoding.
		main.Env = append(main.Env, corev1.EnvVar{Name: "FORGELET_IMAGE_MIRRORS", Value: string(encoded)})
	}
	if a.RegistrySecret != "" {
		pod.Spec.ImagePullSecrets = append(pod.Spec.ImagePullSecrets, corev1.LocalObjectReference{Name: a.RegistrySecret})
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{Name: "act-registry", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: a.RegistrySecret, Items: []corev1.KeyToPath{{Key: ".dockerconfigjson", Path: "config.json"}}}}})
		main.VolumeMounts = append(main.VolumeMounts, corev1.VolumeMount{Name: "act-registry", MountPath: "/var/run/forgelet-act-registry", ReadOnly: true})
		main.Env = append(main.Env, corev1.EnvVar{Name: "FORGELET_REGISTRY_CONFIG", Value: "/var/run/forgelet-act-registry"})
	}
}

// resources accounts for all nested job containers in the Pod-owned engine cgroup.
func (a ActProfile) resources() corev1.ResourceRequirements {
	r := corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("256Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("3"), corev1.ResourceMemory: resource.MustParse("4Gi")}}
	for name, value := range map[corev1.ResourceName]string{corev1.ResourceCPU: a.CPURequest, corev1.ResourceMemory: a.MemoryRequest} {
		if value != "" {
			r.Requests[name] = resource.MustParse(value)
		}
	}
	for name, value := range map[corev1.ResourceName]string{corev1.ResourceCPU: a.CPULimit, corev1.ResourceMemory: a.MemoryLimit} {
		if value != "" {
			r.Limits[name] = resource.MustParse(value)
		}
	}
	return r
}
