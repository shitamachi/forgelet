package run

import "regexp"

var imageRepository = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*(?::[0-9]+)?/[a-z0-9][a-z0-9._/-]*$`)
var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func ValidImage(value string) bool  { return imageRepository.MatchString(value) }
func ValidDigest(value string) bool { return digestPattern.MatchString(value) }
func (p Plan) NeedsBuild() bool {
	for _, step := range p.Steps {
		if step.Action != nil && step.Action.Uses == "forgelet/build@v1" {
			return true
		}
	}
	return false
}
