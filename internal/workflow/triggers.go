package workflow

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/shitamachi/forgelet/internal/run"
)

var ErrNotTriggered = errors.New("workflow does not match event")
var ErrFilesRequired = errors.New("path filters require a complete changed-file list")

func compileTrigger(node *yaml.Node, event run.Event) (run.Event, error) {
	if node == nil {
		return event, fmt.Errorf("workflow requires on: push, pull_request or workflow_dispatch")
	}
	triggers := map[string]*yaml.Node{}
	switch node.Kind {
	case yaml.ScalarNode:
		triggers[node.Value] = nil
	case yaml.SequenceNode:
		for _, n := range node.Content {
			if n.Tag != "!!str" {
				return event, diagnostic(n, "trigger must be a string")
			}
			if _, exists := triggers[n.Value]; exists {
				return event, diagnostic(n, "duplicate trigger")
			}
			triggers[n.Value] = nil
		}
	case yaml.MappingNode:
		var err error
		triggers, err = mapping(node, "push", "pull_request", "workflow_dispatch")
		if err != nil {
			return event, err
		}
	default:
		return event, diagnostic(node, "on must be an event name, list or mapping")
	}
	if len(triggers) == 0 {
		return event, diagnostic(node, "on requires at least one supported event")
	}
	for name, n := range triggers {
		if name != "push" && name != "pull_request" && name != "workflow_dispatch" {
			return event, diagnostic(node, "unsupported trigger %s", name)
		}
		if n == nil || n.Tag == "!!null" {
			continue
		}
		var allowed []string
		switch name {
		case "push":
			allowed = []string{"branches", "branches-ignore", "tags", "tags-ignore", "paths", "paths-ignore"}
		case "pull_request":
			allowed = []string{"types", "branches", "branches-ignore", "paths", "paths-ignore"}
		case "workflow_dispatch":
			allowed = []string{"inputs"}
		}
		f, err := mapping(n, allowed...)
		if err != nil {
			return event, err
		}
		for _, key := range []string{"branches", "tags", "paths"} {
			if f[key] != nil && f[key+"-ignore"] != nil {
				return event, diagnostic(n, "%s and %s-ignore are mutually exclusive", key, key)
			}
			for _, suffix := range []string{"", "-ignore"} {
				if f[key+suffix] != nil {
					if _, err := patterns(f[key+suffix]); err != nil {
						return event, err
					}
				}
			}
		}
	}
	name := event.Name
	if name == "" {
		name = "push"
	}
	n, exists := triggers[name]
	if !exists {
		return event, ErrNotTriggered
	}
	fields := map[string]*yaml.Node{}
	var err error
	if n != nil && n.Tag != "!!null" {
		fields, err = mapping(n)
		if err != nil {
			return event, err
		}
	}
	if name == "workflow_dispatch" {
		event.Name = name
		event.Inputs, err = dispatchInputs(fields["inputs"], event.Inputs)
		return event, err
	}
	if name == "pull_request" {
		types := []string{"opened", "synchronize", "reopened"}
		if fields["types"] != nil {
			types, err = stringList(fields["types"])
			if err != nil {
				return event, err
			}
		}
		matched := false
		for _, kind := range types {
			switch kind {
			case "opened", "synchronize", "reopened", "ready_for_review":
			default:
				return event, diagnostic(fields["types"], "unsupported pull_request type %s", kind)
			}
			matched = matched || kind == event.Action
		}
		if !matched {
			return event, ErrNotTriggered
		}
	}
	ref := strings.TrimPrefix(event.Ref, "refs/heads/")
	family := "branches"
	if name == "pull_request" {
		ref = event.BaseRef
	} else if strings.HasPrefix(event.Ref, "refs/tags/") {
		ref = strings.TrimPrefix(event.Ref, "refs/tags/")
		family = "tags"
	}
	if name == "push" {
		other := "tags"
		if family == "tags" {
			other = "branches"
		}
		if fields[family] == nil && fields[family+"-ignore"] == nil && (fields[other] != nil || fields[other+"-ignore"] != nil) {
			return event, ErrNotTriggered
		}
	}
	matches, err := filter(fields, family, []string{ref})
	if err != nil {
		return event, err
	}
	if !matches {
		return event, ErrNotTriggered
	}
	if family != "tags" && (fields["paths"] != nil || fields["paths-ignore"] != nil) {
		if !event.FilesKnown {
			return event, ErrFilesRequired
		}
		matches, err = filter(fields, "paths", event.Files)
		if err != nil {
			return event, err
		}
		if !matches {
			return event, ErrNotTriggered
		}
	}
	return event, nil
}
func stringList(n *yaml.Node) ([]string, error) {
	if n == nil || n.Kind != yaml.SequenceNode || len(n.Content) == 0 || len(n.Content) > 100 {
		return nil, diagnostic(n, "expected 1–100 strings")
	}
	var values []string
	for _, item := range n.Content {
		s, err := requiredString(item)
		if err != nil {
			return nil, err
		}
		values = append(values, s)
	}
	return values, nil
}
func patterns(n *yaml.Node) ([]string, error) {
	values, err := stringList(n)
	if err != nil {
		return nil, err
	}
	positive := false
	for _, value := range values {
		positive = positive || !strings.HasPrefix(value, "!")
		if _, err := glob(strings.TrimPrefix(value, "!")); err != nil {
			return nil, diagnostic(n, "%s", err)
		}
	}
	if !positive {
		return nil, diagnostic(n, "pattern list requires a positive pattern")
	}
	return values, nil
}
func glob(pattern string) (*regexp.Regexp, error) {
	if pattern == "" || len(pattern) > 256 || strings.ContainsAny(pattern, "[]+\\\x00") {
		return nil, fmt.Errorf("patterns support literals, *, ** and ?; unsupported pattern %q", pattern)
	}
	var out strings.Builder
	out.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					out.WriteString("(?:.*/)?")
				} else {
					out.WriteString(".*")
				}
			} else {
				out.WriteString("[^/]*")
			}
		case '?':
			out.WriteString("[^/]")
		default:
			out.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	out.WriteString("$")
	return regexp.Compile(out.String())
}
func filter(fields map[string]*yaml.Node, key string, values []string) (bool, error) {
	n := fields[key]
	ignore := false
	if n == nil {
		n = fields[key+"-ignore"]
		ignore = n != nil
	}
	if n == nil {
		return true, nil
	}
	ps, err := patterns(n)
	if err != nil {
		return false, err
	}
	for _, value := range values {
		match := false
		for _, p := range ps {
			negative := strings.HasPrefix(p, "!")
			r, _ := glob(strings.TrimPrefix(p, "!"))
			if r.MatchString(value) {
				match = !negative
			}
		}
		if match != ignore {
			return true, nil
		}
	}
	return false, nil
}

func dispatchInputs(n *yaml.Node, supplied map[string]any) (map[string]any, error) {
	if n == nil {
		if len(supplied) > 0 {
			return nil, fmt.Errorf("workflow declares no inputs")
		}
		return nil, nil
	}
	fields, err := mapping(n)
	if err != nil {
		return nil, err
	}
	if len(fields) > 25 {
		return nil, diagnostic(n, "at most 25 manual inputs")
	}
	result := map[string]any{}
	for key, input := range fields {
		if !identifier.MatchString(key) {
			return nil, diagnostic(input, "invalid input name")
		}
		f, err := mapping(input, "description", "required", "default", "type", "options")
		if err != nil {
			return nil, err
		}
		kind, err := optionalString(f["type"], "string")
		if err != nil {
			return nil, err
		}
		required := false
		if f["required"] != nil {
			if f["required"].Tag != "!!bool" {
				return nil, diagnostic(input, "input required must be boolean")
			}
			required = f["required"].Value == "true"
		}
		value, exists := supplied[key]
		if !exists && f["default"] != nil {
			value, err = matrixScalar(f["default"])
			if err != nil {
				return nil, err
			}
			exists = true
		}
		if !exists && required {
			return nil, diagnostic(input, "required input %s is missing", key)
		}
		switch kind {
		case "string", "choice":
			if !exists {
				value = ""
			}
			s, ok := value.(string)
			if !ok {
				return nil, diagnostic(input, "input %s requires a string", key)
			}
			if len(s) > 8192 {
				return nil, diagnostic(input, "input too large")
			}
			if kind == "choice" {
				choices, err := stringList(f["options"])
				if err != nil {
					return nil, err
				}
				found := false
				for _, choice := range choices {
					found = found || choice == s
				}
				if !found {
					return nil, diagnostic(input, "input %s is not a declared choice", key)
				}
			}
		case "boolean":
			if !exists {
				value = false
			}
			if _, ok := value.(bool); !ok {
				return nil, diagnostic(input, "input %s requires a boolean", key)
			}
		case "number":
			if !exists {
				value = float64(0)
			}
			if _, ok := value.(float64); !ok {
				return nil, diagnostic(input, "input %s requires a number", key)
			}
		default:
			return nil, diagnostic(input, "unsupported input type %s", kind)
		}
		result[key] = value
	}
	for key := range supplied {
		if _, ok := fields[key]; !ok {
			return nil, diagnostic(n, "unknown manual input %s", key)
		}
	}
	return result, nil
}
