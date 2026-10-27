package workflow

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v4"

	"github.com/shitamachi/forgelet/internal/expression"
)

func compileDependencies(jobs map[string]*yaml.Node) (map[string][]string, error) {
	dependencies := make(map[string][]string, len(jobs))
	for _, key := range sortedKeys(jobs) {
		fields, err := mapping(jobs[key])
		if err != nil {
			return nil, fmt.Errorf("job %s: %w", key, err)
		}
		dependencies[key], err = parseNeeds(fields["needs"], key, jobs)
		if err != nil {
			return nil, fmt.Errorf("job %s: %w", key, err)
		}
	}
	visited := make(map[string]byte, len(jobs))
	var visit func(string) error
	visit = func(key string) error {
		if visited[key] == 1 {
			return diagnostic(jobs[key], "dependency cycle includes job %q", key)
		}
		if visited[key] == 2 {
			return nil
		}
		visited[key] = 1
		for _, need := range dependencies[key] {
			if err := visit(need); err != nil {
				return err
			}
		}
		visited[key] = 2
		return nil
	}
	for _, key := range sortedKeys(jobs) {
		if err := visit(key); err != nil {
			return nil, err
		}
	}
	return dependencies, nil
}

func parseNeeds(node *yaml.Node, key string, jobs map[string]*yaml.Node) ([]string, error) {
	if node == nil {
		return nil, nil
	}
	items := []*yaml.Node{node}
	if node.Kind == yaml.SequenceNode {
		items = node.Content
	} else if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return nil, diagnostic(node, "needs must be a job identifier or sequence of job identifiers")
	}
	if len(items) > 64 {
		return nil, diagnostic(node, "needs cannot contain more than 64 jobs")
	}
	seen := make(map[string]bool, len(items))
	needs := make([]string, 0, len(items))
	for _, item := range items {
		need, err := requiredString(item)
		if err != nil {
			return nil, err
		}
		if !identifier.MatchString(need) {
			return nil, diagnostic(item, "needs requires static job identifiers")
		}
		if need == key {
			return nil, diagnostic(item, "job cannot depend on itself")
		}
		if _, exists := jobs[need]; !exists {
			return nil, diagnostic(item, "unknown dependency %q", need)
		}
		if seen[need] {
			return nil, diagnostic(item, "duplicate dependency %q", need)
		}
		seen[need] = true
		needs = append(needs, need)
	}
	return needs, nil
}

type referenceScope struct {
	needs   map[string]bool
	steps   map[string]bool
	env     bool
	matrix  bool
	secrets bool
	action  bool
}

func (scope referenceScope) allowed(reference string) bool {
	parts := strings.Split(reference, ".")
	if reference == "env" {
		return scope.env
	}
	if reference == "vars" {
		return true
	}
	if reference == "needs" {
		return scope.needs != nil
	}
	if reference == "steps" {
		return scope.steps != nil
	}
	if len(parts) >= 2 && parts[0] == "needs" && parts[1] == "*" {
		return len(scope.needs) > 0 && (len(parts) == 2 || parts[2] == "result" || parts[2] == "outputs")
	}
	if len(parts) == 3 && parts[0] == "needs" && scope.needs[parts[1]] && parts[2] == "outputs" {
		return true
	}
	if len(parts) == 3 && parts[0] == "steps" && scope.steps[parts[1]] && parts[2] == "outputs" {
		return true
	}

	if reference == "github" || strings.HasPrefix(reference, "github.event.") || reference == "github.event" {
		return true
	}
	if len(parts) >= 3 && parts[0] == "inputs" && identifier.MatchString(parts[1]) {
		return true
	}

	if reference == "matrix" {
		return scope.matrix
	}
	if reference == "inputs" {
		return true
	}
	if len(parts) == 2 {
		if (parts[0] == "vars" || parts[0] == "inputs" || parts[0] == "matrix" && scope.matrix || parts[0] == "secrets" && scope.secrets) && identifier.MatchString(parts[1]) {
			return true
		}
		if parts[0] == "strategy" && scope.matrix {
			switch parts[1] {
			case "job-index", "job-total", "max-parallel", "fail-fast":
				return true
			}
		}
		if parts[0] == "runner" {
			switch parts[1] {
			case "os", "arch", "temp", "tool_cache":
				return scope.env
			}
		}
	}
	if len(parts) == 2 {
		if parts[0] == "github" {
			if parts[1] == "action_path" {
				return scope.action
			}
			if parts[1] == "server_url" || parts[1] == "api_url" {
				return true
			}
			switch parts[1] {
			case "sha", "ref", "ref_name", "ref_type", "repository", "event_name", "actor", "head_ref", "base_ref", "workflow", "repository_owner", "run_id", "job":
				return true
			}
		}
		return parts[0] == "env" && scope.env && envIdentifier.MatchString(parts[1])
	}
	if len(parts) == 3 {
		if parts[0] == "needs" && scope.needs[parts[1]] && parts[2] == "result" {
			return true
		}
		return parts[0] == "steps" && scope.steps[parts[1]] && (parts[2] == "outcome" || parts[2] == "conclusion")
	}
	if len(parts) == 4 && parts[2] == "outputs" && identifier.MatchString(parts[3]) {
		return (parts[0] == "steps" && scope.steps[parts[1]]) || (parts[0] == "needs" && scope.needs[parts[1]])
	}
	return false
}

func condition(node *yaml.Node, scope referenceScope) (string, error) {
	if node == nil {
		return "", nil
	}
	value := "null"
	if node.Tag != "!!null" {
		var err error
		value, err = scalar(node)
		if err != nil {
			return "", err
		}
	}
	if err := expression.Validate(value, scope.allowed); err != nil {
		return "", diagnostic(node, "invalid condition: %s", err)
	}
	return value, nil
}

func jobOutputs(node *yaml.Node, scope referenceScope) (map[string]string, error) {
	if node == nil {
		return nil, nil
	}
	fields, err := mapping(node)
	if err != nil {
		return nil, err
	}
	if len(fields) > 100 {
		return nil, diagnostic(node, "job outputs cannot contain more than 100 keys")
	}
	outputs := make(map[string]string, len(fields))
	size := 0
	for _, key := range sortedKeys(fields) {
		value := fields[key]
		if !identifier.MatchString(key) {
			return nil, diagnostic(value, "invalid output identifier %q", key)
		}
		text, err := scalar(value)
		if err != nil {
			return nil, err
		}
		if err := validateExpressions(value, scope); err != nil {
			return nil, err
		}
		size += len(key) + len(text)
		if size > 64<<10 {
			return nil, diagnostic(node, "job outputs exceed 64 KiB")
		}
		outputs[key] = text
	}
	return outputs, nil
}
