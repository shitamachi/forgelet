package run

import (
	"fmt"
	"regexp"

	"github.com/distribution/reference"
)

// PinnedImage accepts only explicit registry references with a SHA256 digest.
func PinnedImage(image string) bool {
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return false
	}
	pinned, ok := named.(reference.Canonical)
	return ok && pinned.Digest().Algorithm() == "sha256" && pinned.Digest().Validate() == nil
}

// ValidateImageMirrors describes administrator-owned Docker Hub aliases. Sources
// must use the runner registry; its pull credential never enters the job.
func ValidateImageMirrors(runner string, images map[string]string) error {
	if len(images) > 16 {
		return fmt.Errorf("at most 16 image mirrors are allowed")
	}
	if len(images) == 0 {
		return nil
	}
	runnerName, err := reference.ParseNormalizedNamed(runner)
	if err != nil {
		return fmt.Errorf("invalid runner image: %w", err)
	}
	seen := map[string]bool{}
	for alias, source := range images {
		name, err := reference.ParseNormalizedNamed(alias)
		if err != nil {
			return fmt.Errorf("invalid mirrored alias %q: %w", alias, err)
		}
		_, tagged := name.(reference.Tagged)
		_, pinned := name.(reference.Canonical)
		if reference.Domain(name) != "docker.io" || !tagged || pinned || seen[name.String()] {
			return fmt.Errorf("image mirror alias %q must be a unique Docker Hub tag", alias)
		}
		seen[name.String()] = true
		src, err := reference.ParseNormalizedNamed(source)
		if err != nil || !PinnedImage(source) {
			return fmt.Errorf("image mirror %q requires a SHA256 pinned source", alias)
		}
		if reference.Domain(src) != reference.Domain(runnerName) {
			return fmt.Errorf("image mirror %q must use the runner registry", alias)
		}
	}
	return nil
}

var imageGroupName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func ValidImageGroup(name string) bool { return imageGroupName.MatchString(name) }
