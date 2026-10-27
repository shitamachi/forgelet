package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/shitamachi/forgelet/internal/run"
)

type actMetadata struct{ bytes.Buffer }

func (b *actMetadata) Write(data []byte) (int, error) {
	if b.Len()+len(data) > 64<<10 {
		return 0, fmt.Errorf("image metadata exceeds limit")
	}
	return b.Buffer.Write(data)
}

// A Docker archive import retains the config digest but can lose RepoDigests.
// Verify the pinned registry manifest before reusing the complete local image.
// Tags, indexes and any failed verification use the normal pull path.
func preloadedActImage(ctx context.Context, opts Options) (string, bool) {
	at := strings.LastIndex(opts.ActImage, "@sha256:")
	if at < 1 || !run.ValidDigest(opts.ActImage[at+1:]) {
		return "", false
	}
	configuration, err := os.MkdirTemp("", "forgelet-image-inspect-")
	if err != nil {
		return "", false
	}
	defer func() { _ = os.RemoveAll(configuration) }()
	lookup, done := context.WithTimeout(ctx, 10*time.Second)
	defer done()
	var manifest actMetadata
	cmd := exec.CommandContext(lookup, "docker", "buildx", "imagetools", "inspect", "--raw", opts.ActImage)
	cmd.Env = append(os.Environ(), "DOCKER_CONFIG="+opts.RegistryConfig, "BUILDX_CONFIG="+configuration)
	cmd.Stdout = &manifest
	cmd.Stderr = io.Discard
	if code, err := runProcess(lookup, cmd); err != nil || code != 0 {
		return "", false
	}
	hash := sha256.Sum256(manifest.Bytes())
	if "sha256:"+hex.EncodeToString(hash[:]) != opts.ActImage[at+1:] {
		return "", false
	}
	var description struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if err := json.Unmarshal(manifest.Bytes(), &description); err != nil || !run.ValidDigest(description.Config.Digest) {
		return "", false
	}
	var local actMetadata
	cmd = exec.CommandContext(lookup, "docker", "image", "inspect", "--format={{.Id}}", description.Config.Digest)
	cmd.Stdout = &local
	cmd.Stderr = io.Discard
	if code, err := runProcess(lookup, cmd); err != nil || code != 0 || strings.TrimSpace(local.String()) != description.Config.Digest {
		return "", false
	}
	return description.Config.Digest, true
}
