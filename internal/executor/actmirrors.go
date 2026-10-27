package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
)

// Pull immutable copies using only the supervisor's registry config. Register
// their ordinary public tags in this attempt's fresh engine, without changing
// workflows, Dockerfiles or Testcontainers defaults. Never silently fall back.
func prepareActMirrors(ctx context.Context, opts Options, sink *logSink) error {
	aliases := make([]string, 0, len(opts.ImageMirrors))
	for alias := range opts.ImageMirrors {
		aliases = append(aliases, alias)
	}
	slices.Sort(aliases)
	for _, alias := range aliases {
		source := opts.ImageMirrors[alias]
		if _, err := fmt.Fprintf(sink, "Preparing mirrored image %s from %s\n", alias, source); err != nil {
			return err
		}
		pull := exec.CommandContext(ctx, "docker", "pull", source)
		pull.Env = append(os.Environ(), "DOCKER_CONFIG="+opts.RegistryConfig)
		pull.Stdout, pull.Stderr = sink, sink
		if code, err := runProcess(ctx, pull); err != nil || code != 0 {
			return fmt.Errorf("pull configured image mirror %s", alias)
		}
		tag := exec.CommandContext(ctx, "docker", "tag", source, alias)
		tag.Stdout, tag.Stderr = sink, sink
		if code, err := runProcess(ctx, tag); err != nil || code != 0 {
			return fmt.Errorf("register configured image alias %s", alias)
		}
		// Check the complete config identity after tagging, not just a short ID
		// or tag spelling. The digest pull already validated the manifest/blobs.
		var output actMetadata
		inspect := exec.CommandContext(ctx, "docker", "image", "inspect", "--format={{.Id}}", source, alias)
		inspect.Stdout, inspect.Stderr = &output, sink
		code, err := runProcess(ctx, inspect)
		ids := strings.Fields(output.String())
		if err != nil || code != 0 || len(ids) != 2 || ids[0] != ids[1] || !strings.HasPrefix(ids[0], "sha256:") {
			return fmt.Errorf("verify configured image alias %s", alias)
		}
	}
	return sink.finish()
}
