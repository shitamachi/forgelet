package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/shitamachi/forgelet/internal/run"
)

func (s *actionSession) build(ctx context.Context, plan run.Plan, in, env, files map[string]string, sink *logSink) (int, error) {
	build := plan.ExternalBuild
	if (plan.External || build != nil) && (build == nil || !build.Valid() || build.Image != in["image"]) {
		return -1, fmt.Errorf("build requires an authorized image repository and platform")
	}
	if !plan.External && (s.opts.BuildAddress == "" || !run.ValidImage(in["image"]) || !slices.Contains(s.opts.BuildImages, in["image"])) {
		return -1, fmt.Errorf("build requires an enabled profile and an allowed image repository")
	}
	contextPath, err := withinWorkspace(s.workspace, in["context"], false)
	if err != nil {
		return -1, err
	}
	dockerfile, err := withinWorkspace(s.workspace, in["file"], false)
	if err != nil {
		return -1, err
	}
	metadata, err := os.CreateTemp(s.meta, "build-result-")
	if err != nil {
		return -1, err
	}
	metadataPath := metadata.Name()
	_ = metadata.Close()
	defer func() { _ = os.Remove(metadataPath) }()
	// Immutable output is returned from BuildKit's exporter, never inferred from
	// a mutable tag. The per-attempt tag only provides a registry upload name.
	image := in["image"] + ":forgelet-" + plan.JobID
	exporter := "type=image,name=" + image + ",push=true"
	if build != nil && build.InsecureRegistry || build == nil && s.opts.BuildInsecure {
		exporter += ",registry.insecure=true"
	}
	binary := s.opts.Buildctl
	if binary == "" {
		binary = "/usr/local/bin/buildctl"
	}
	args := []string{"--addr", s.opts.BuildAddress, "build", "--frontend", "dockerfile.v0", "--local", "context=" + contextPath, "--local", "dockerfile=" + filepath.Dir(dockerfile), "--opt", "filename=" + filepath.Base(dockerfile), "--output", exporter, "--metadata-file", metadataPath, "--progress", "plain"}
	if plan.External {
		binary = "docker"
		args = []string{"buildx", "build", "--platform", build.Platform, "--file", dockerfile, "--output", exporter, "--metadata-file", metadataPath, "--progress", "plain", "--provenance=false", "--sbom=false"}
	} else if build != nil {
		args = append(args, "--opt", "platform="+build.Platform)
	}
	if target := in["target"]; target != "" {
		if strings.ContainsAny(target, "\r\n\x00") {
			return -1, fmt.Errorf("invalid build target")
		}
		if plan.External {
			args = append(args, "--target", target)
		} else {
			args = append(args, "--opt", "target="+target)
		}
	}
	for _, line := range strings.Split(in["build-args"], "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, _, ok := strings.Cut(line, "=")
		if !ok || !envKey.MatchString(key) {
			return -1, fmt.Errorf("build-args must contain NAME=value lines")
		}
		if build != nil {
			switch key {
			case "TARGETPLATFORM", "TARGETOS", "TARGETARCH", "TARGETVARIANT":
				return -1, fmt.Errorf("build-args cannot override the controller's target platform")
			}
		}
		if plan.External {
			args = append(args, "--build-arg", line)
		} else {
			args = append(args, "--opt", "build-arg:"+line)
		}
	}
	if plan.External {
		args = append(args, "--", contextPath)
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = s.workspace
	if !plan.External {
		env = copyEnvironment(env)
		if s.opts.RegistryConfig != "" {
			env["DOCKER_CONFIG"] = s.opts.RegistryConfig
		}
		cmd.Env = environmentList(env)
	}
	cmd.Stdout = sink
	cmd.Stderr = sink
	code, err := runProcess(ctx, cmd)
	if err != nil || code != 0 {
		return code, err
	}
	data, err := os.ReadFile(metadataPath)
	if err != nil || len(data) > 1<<20 {
		return -1, fmt.Errorf("build metadata missing or too large")
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(data, &meta); err != nil {
		return -1, fmt.Errorf("invalid build metadata: %w", err)
	}
	var digest string
	if json.Unmarshal(meta["containerimage.digest"], &digest) != nil || !run.ValidDigest(digest) {
		return -1, fmt.Errorf("BuildKit did not return an immutable digest")
	}
	return 0, builtinOutput(files, map[string]string{"digest": digest, "image": in["image"] + "@" + digest})
}
