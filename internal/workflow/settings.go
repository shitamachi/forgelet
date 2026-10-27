package workflow

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v4"

	"github.com/shitamachi/forgelet/internal/run"
)

func compilePermissions(n *yaml.Node, parent map[string]string) (map[string]string, error) {
	if n == nil {
		return parent, nil
	}
	permissions := map[string]string{}
	if n.Kind == yaml.ScalarNode {
		if n.Value != "read-all" {
			return nil, diagnostic(n, "execution capability permissions %q is not implemented", n.Value)
		}
		return nil, diagnostic(n, "read-all requires token capabilities not implemented")
	}
	fields, err := mapping(n)
	if err != nil {
		return nil, err
	}
	for key, v := range fields {
		if key != "contents" && key != "packages" || key == "contents" && v.Value != "read" && v.Value != "none" || key == "packages" && v.Value != "write" && v.Value != "none" {
			return nil, diagnostic(v, "execution capability permission %s:%s is not implemented", key, v.Value)
		}
		permissions[key] = v.Value
	}
	return permissions, nil
}
func permissionCeiling(permissions, parent map[string]string) error {
	if parent != nil && parent["contents"] != "read" && (permissions == nil || permissions["contents"] == "read") {
		return fmt.Errorf("called workflow cannot elevate contents permission")
	}
	if parent != nil && parent["packages"] != "write" && permissions["packages"] == "write" {
		return fmt.Errorf("called workflow cannot elevate packages permission")
	}
	return nil
}
func applyDefaults(p *run.Plan, steps *yaml.Node, nodes ...*yaml.Node) error {
	shell, dir := "", ""
	for _, n := range nodes {
		if n == nil {
			continue
		}
		fields, err := mapping(n, "run")
		if err != nil {
			return err
		}
		settings, err := mapping(fields["run"], "shell", "working-directory")
		if err != nil {
			return err
		}
		if value := settings["shell"]; value != nil {
			shell, err = requiredString(value)
			if err != nil {
				return err
			}
			if shell != "bash" && shell != "sh" {
				return diagnostic(value, "unsupported default shell %q", shell)
			}
		}
		if value := settings["working-directory"]; value != nil {
			dir, err = requiredString(value)
			if !strings.Contains(dir, "${{") && !safeRelativePath(dir) {
				return diagnostic(value, "default working-directory must stay inside the workspace")
			}
			if err != nil {
				return err
			}
			if err = validateExpressions(value, referenceScope{env: true, matrix: true}); err != nil {
				return err
			}
		}
	}
	for i := range p.Steps {
		step := &p.Steps[i]
		if step.Run == "" {
			continue
		}
		raw, _ := mapping(steps.Content[i])
		if raw["shell"] == nil && shell != "" {
			step.Shell = shell
		}
		if raw["working-directory"] == nil {
			step.WorkingDirectory = dir
		}
	}
	return nil
}
