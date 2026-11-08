package run

import "regexp"

var imageRepository = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*(?::[0-9]+)?/[a-z0-9][a-z0-9._/-]*$`)
var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func ValidImage(value string) bool  { return imageRepository.MatchString(value) }
func ValidDigest(value string) bool { return digestPattern.MatchString(value) }

const ExternalImageDigestOutput = "forgelet_image_digest"
const RunnerBuildPlatformsHeader = "X-Forgelet-Build-Platforms"

// ExternalBuild is the controller-owned image destination and target platform.
type ExternalBuild struct {
	Image            string `yaml:"image" json:"image"`
	Platform         string `yaml:"platform" json:"platform"`
	InsecureRegistry bool   `yaml:"insecure_registry,omitempty" json:"insecure_registry,omitempty"`
}

func (b ExternalBuild) Valid() bool {
	return ValidImage(b.Image) && ValidBuildPlatform(b.Platform)
}

func ValidBuildPlatform(platform string) bool {
	return platform == "linux/amd64" || platform == "linux/arm64"
}

func (p Plan) NeedsBuild() bool {
	for _, step := range p.Steps {
		if step.Action != nil && step.Action.Uses == "forgelet/build@v1" {
			return true
		}
	}
	return false
}
