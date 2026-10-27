package action

import (
	"fmt"

	"github.com/shitamachi/forgelet/internal/run"
)

// ValidateNative keeps the legacy runtime limits separate from standard syntax.
func ValidateNative(ref *run.Action) error {
	if ref.Repository != "" && !shaPattern.MatchString(ref.SHA) {
		return fmt.Errorf("mutable Action references require an act execution profile")
	}
	if ref.Repository == "actions/setup-go" {
		if ref.SHA != SetupGoSHA {
			return fmt.Errorf("setup-go version has not been validated")
		}
		if ref.Inputs["cache"] != "false" {
			return fmt.Errorf("setup-go requires cache: false")
		}
		for key := range ref.Inputs {
			if key != "go-version" && key != "cache" && key != "token" {
				return fmt.Errorf("unsupported setup-go input %q", key)
			}
		}
		if ref.Inputs["go-version"] == "" || ref.Inputs["token"] != "" {
			return fmt.Errorf("setup-go requires go-version and an empty token")
		}
	}
	if ref.Repository == "actions/setup-node" {
		if ref.SHA != SetupNodeSHA {
			return fmt.Errorf("setup-node version has not been validated")
		}
		if ref.Inputs["package-manager-cache"] != "false" || ref.Inputs["cache"] != "" {
			return fmt.Errorf("setup-node requires package-manager-cache: false and no cache")
		}
		for key := range ref.Inputs {
			if key != "node-version" && key != "package-manager-cache" && key != "cache" && key != "token" {
				return fmt.Errorf("unsupported setup-node input %q", key)
			}
		}
		if ref.Inputs["node-version"] == "" || ref.Inputs["token"] != "" {
			return fmt.Errorf("setup-node requires node-version and an empty token")
		}
	}
	return nil
}
