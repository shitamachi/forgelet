// Package noderuntime selects a real Node binary from an Action's declaration.
package noderuntime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

func Select(args []string, workspace string) (string, error) {
	version := "node24"
	if len(args) > 0 && strings.HasPrefix(args[0], "-") {
		return version, nil
	}
	if len(args) == 0 {
		return version, nil
	}
	directory, err := filepath.Abs(filepath.Dir(args[0]))
	if err != nil {
		return "", err
	}
	for {
		for _, name := range []string{"action.yml", "action.yaml"} {
			data, err := os.ReadFile(filepath.Join(directory, name))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return "", err
			}
			if len(data) > 256<<10 {
				return "", fmt.Errorf("action metadata exceeds limit")
			}
			var meta struct {
				Runs struct {
					Using string `yaml:"using"`
					Pre   string `yaml:"pre"`
				} `yaml:"runs"`
			}
			if err := yaml.Unmarshal(data, &meta); err != nil {
				return "", fmt.Errorf("read Action runtime: %w", err)
			}
			if meta.Runs.Pre != "" && workspace != "" {
				relative, err := filepath.Rel(workspace, directory)
				if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
					return "", fmt.Errorf("act does not support pre for workspace-local Actions; use a native profile")
				}
			}
			switch meta.Runs.Using {
			case "node20", "node24":
				return meta.Runs.Using, nil
			case "composite", "docker":
				return version, nil
			default:
				return "", fmt.Errorf("unsupported Action runtime %q", meta.Runs.Using)
			}
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return version, nil
		}
		directory = parent
	}
}
