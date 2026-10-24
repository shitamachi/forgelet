package workflow

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/shitamachi/forgelet/internal/expression"
	"github.com/shitamachi/forgelet/internal/run"
)

type strategy struct {
	values   []map[string]any
	parallel int
	failFast bool
}

func compileStrategy(node *yaml.Node) (strategy, error) {
	result := strategy{values: []map[string]any{nil}, failFast: true}
	if node == nil {
		return result, nil
	}
	fields, err := mapping(node, "matrix", "max-parallel", "fail-fast")
	if err != nil {
		return result, err
	}
	if n := fields["max-parallel"]; n != nil {
		if n.Tag != "!!int" {
			return result, diagnostic(n, "max-parallel must be an integer")
		}
		result.parallel, err = strconv.Atoi(n.Value)
		if err != nil || result.parallel < 1 || result.parallel > 256 {
			return result, diagnostic(n, "max-parallel must be 1–256")
		}
	}
	if n := fields["fail-fast"]; n != nil {
		if n.Tag != "!!bool" {
			return result, diagnostic(n, "fail-fast must be boolean")
		}
		result.failFast = n.Value == "true"
	}
	matrix, err := mapping(fields["matrix"])
	if err != nil {
		return result, fmt.Errorf("static matrix: %w", err)
	}
	axes := map[string]bool{}
	original := []map[string]any{{}}
	for _, key := range sortedKeys(matrix) {
		if key == "include" || key == "exclude" {
			continue
		}
		if !identifier.MatchString(key) {
			return result, diagnostic(matrix[key], "invalid matrix axis")
		}
		axes[key] = true
		axis := matrix[key]
		if axis.Kind != yaml.SequenceNode || len(axis.Content) == 0 {
			return result, diagnostic(axis, "matrix axis must be a nonempty scalar array")
		}
		var expanded []map[string]any
		for _, base := range original {
			for _, n := range axis.Content {
				v, err := matrixScalar(n)
				if err != nil {
					return result, err
				}
				item := cloneMatrix(base)
				item[key] = v
				expanded = append(expanded, item)
				if len(expanded) > 256 {
					return result, diagnostic(node, "matrix exceeds 256 combinations")
				}
			}
		}
		original = expanded
	}
	excludes, err := matrixEntries(matrix["exclude"])
	if err != nil {
		return result, err
	}
	var bases []map[string]any
	for _, base := range original {
		drop := false
		for _, exclude := range excludes {
			match := true
			for key, value := range exclude {
				if !axes[key] {
					return result, diagnostic(matrix["exclude"], "exclude refers to unknown axis %s", key)
				}
				match = match && reflect.DeepEqual(base[key], value)
			}
			drop = drop || match
		}
		if !drop {
			bases = append(bases, base)
		}
	}
	if len(axes) == 0 {
		bases = nil
	}
	result.values = make([]map[string]any, len(bases))
	for i, b := range bases {
		result.values[i] = cloneMatrix(b)
	}
	includes, err := matrixEntries(matrix["include"])
	if err != nil {
		return result, err
	}
	for _, include := range includes {
		matched := false
		for i, base := range bases {
			fits := true
			for key, value := range include {
				if axes[key] && !reflect.DeepEqual(base[key], value) {
					fits = false
				}
			}
			if fits {
				for key, value := range include {
					result.values[i][key] = value
				}
				matched = true
			}
		}
		if !matched {
			result.values = append(result.values, cloneMatrix(include))
		}
	}
	if len(result.values) == 0 || len(result.values) > 256 {
		return result, diagnostic(node, "matrix must expand to 1–256 jobs")
	}
	return result, nil
}
func matrixScalar(n *yaml.Node) (any, error) {
	if n.Kind != yaml.ScalarNode || strings.Contains(n.Value, "${{") {
		return nil, diagnostic(n, "matrix values must be static string, number or boolean")
	}
	switch n.Tag {
	case "!!str":
		return n.Value, nil
	case "!!bool":
		return n.Value == "true", nil
	case "!!int", "!!float":
		v, err := strconv.ParseFloat(n.Value, 64)
		if err == nil {
			return v, nil
		}
	}
	return nil, diagnostic(n, "unsupported matrix value")
}
func matrixEntries(n *yaml.Node) ([]map[string]any, error) {
	if n == nil {
		return nil, nil
	}
	if n.Kind != yaml.SequenceNode || len(n.Content) > 256 {
		return nil, diagnostic(n, "matrix include/exclude must be an array of at most 256 mappings")
	}
	var result []map[string]any
	for _, entry := range n.Content {
		m, err := mapping(entry)
		if err != nil {
			return nil, err
		}
		item := map[string]any{}
		for _, k := range sortedKeys(m) {
			if !identifier.MatchString(k) {
				return nil, diagnostic(m[k], "invalid matrix key")
			}
			v, err := matrixScalar(m[k])
			if err != nil {
				return nil, err
			}
			item[k] = v
		}
		result = append(result, item)
	}
	return result, nil
}
func cloneMatrix(m map[string]any) map[string]any {
	n := make(map[string]any, len(m))
	for k, v := range m {
		n[k] = v
	}
	return n
}

func staticContext(event run.Event, matrix map[string]any) expression.Context {
	p := run.Plan{Repository: event.Repository, SHA: event.SHA, Ref: event.Ref, Event: event.Payload, EventName: event.Name, BaseRef: event.BaseRef, Inputs: event.Inputs, Vars: event.Vars, Matrix: matrix}
	values, typed := p.Contexts()
	return expression.Context{Values: values, Typed: typed, Success: true}
}
func staticString(n *yaml.Node, fallback string, ctx expression.Context, scope referenceScope) (string, error) {
	s, err := optionalString(n, fallback)
	if err != nil {
		return "", err
	}
	if err := expression.ValidateTemplate(s, scope.allowed); err != nil {
		return "", diagnostic(n, "invalid early expression: %s", err)
	}
	return expression.Interpolate(s, ctx)
}
func booleanSetting(n *yaml.Node, ctx expression.Context, scope referenceScope) (bool, error) {
	if n == nil {
		return false, nil
	}
	if n.Tag == "!!bool" {
		return n.Value == "true", nil
	}
	if n.Tag != "!!str" || !strings.HasPrefix(strings.TrimSpace(n.Value), "${{") {
		return false, diagnostic(n, "expected boolean or boolean expression")
	}
	if err := expression.Validate(n.Value, scope.allowed); err != nil {
		return false, diagnostic(n, "invalid expression: %s", err)
	}
	v, err := expression.Eval(n.Value, ctx)
	if err != nil {
		return false, err
	}
	b, ok := v.(bool)
	if !ok {
		return false, diagnostic(n, "expression must return boolean")
	}
	return b, nil
}
func concurrency(n *yaml.Node, ctx expression.Context, scope referenceScope) (string, bool, error) {
	if n == nil {
		return "", false, nil
	}
	groupNode := n
	cancel := false
	if n.Kind == yaml.MappingNode {
		f, err := mapping(n, "group", "cancel-in-progress")
		if err != nil {
			return "", false, err
		}
		groupNode = f["group"]
		cancel, err = booleanSetting(f["cancel-in-progress"], ctx, scope)
		if err != nil {
			return "", false, err
		}
	}
	group, err := staticString(groupNode, "", ctx, scope)
	if err != nil {
		return "", false, err
	}
	if strings.TrimSpace(group) == "" || len(group) > 256 {
		return "", false, diagnostic(n, "concurrency group must contain 1–256 bytes")
	}
	return strings.ToLower(group), cancel, nil
}

func staticWorkflowContext(event run.Event, name, runID string) expression.Context {
	ctx := staticContext(event, nil)
	ctx.Values["github.workflow"] = name
	ctx.Values["github.run_id"] = runID
	return ctx
}
