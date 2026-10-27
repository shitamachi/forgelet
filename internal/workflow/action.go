package workflow

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"go.yaml.in/yaml/v4"

	"github.com/shitamachi/forgelet/internal/expression"
	"github.com/shitamachi/forgelet/internal/run"
)

type ActionDefinition struct {
	Inputs                                map[string]string
	Outputs                               map[string]string
	Using, Main, Pre, Post, PreIf, PostIf string
	Steps                                 []run.Step
}

func ParseAction(source []byte) (ActionDefinition, error) {
	var result ActionDefinition
	if len(source) > 256<<10 {
		return result, fmt.Errorf("action metadata exceeds 256 KiB")
	}
	var doc, extra yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(source))
	if err := dec.Decode(&doc); err != nil {
		return result, err
	}
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return result, fmt.Errorf("action must contain one YAML document")
	}
	count := 0
	if err := validateTree(&doc, 0, &count); err != nil {
		return result, err
	}
	if len(doc.Content) != 1 {
		return result, fmt.Errorf("action must be a mapping")
	}
	fields, err := mapping(doc.Content[0], "name", "description", "author", "branding", "inputs", "outputs", "runs")
	if err != nil {
		return result, err
	}
	for _, key := range []string{"name", "description"} {
		if _, err := requiredString(fields[key]); err != nil {
			return result, fmt.Errorf("action %s: %w", key, err)
		}
	}
	result.Inputs = map[string]string{}
	if n := fields["inputs"]; n != nil {
		inputs, err := mapping(n)
		if err != nil {
			return result, err
		}
		if len(inputs) > 100 {
			return result, diagnostic(n, "at most 100 action inputs")
		}
		for name, input := range inputs {
			if !identifier.MatchString(name) {
				return result, diagnostic(input, "invalid input name")
			}
			f, err := mapping(input, "description", "required", "default", "deprecationMessage")
			if err != nil {
				return result, err
			}
			value := ""
			if f["default"] != nil {
				value, err = scalar(f["default"])
				if err != nil {
					return result, err
				}
			}
			result.Inputs[name] = value
		}
	}
	runs, err := mapping(fields["runs"], "using", "main", "pre", "post", "pre-if", "post-if", "steps")
	if err != nil {
		return result, err
	}
	result.Using, err = requiredString(runs["using"])
	if err != nil {
		return result, err
	}
	scope := referenceScope{env: true, matrix: true, action: true, steps: map[string]bool{}}
	switch result.Using {
	case "node20", "node24":
		if runs["steps"] != nil {
			return result, diagnostic(runs["steps"], "JavaScript action cannot declare steps")
		}
		for key, dest := range map[string]*string{"main": &result.Main, "pre": &result.Pre, "post": &result.Post} {
			*dest, err = optionalString(runs[key], "")
			if err != nil {
				return result, err
			}
			if *dest != "" && (!safeRelativePath(*dest) || strings.Contains(*dest, "${{")) {
				return result, diagnostic(runs[key], "action script must stay in action directory")
			}
		}
		if result.Main == "" {
			return result, fmt.Errorf("JavaScript action requires main")
		}
		result.PreIf, err = optionalString(runs["pre-if"], "always()")
		if err != nil {
			return result, err
		}
		result.PostIf, err = optionalString(runs["post-if"], "always()")
		if err != nil {
			return result, err
		}
		for _, condition := range []string{result.PreIf, result.PostIf} {
			if err := expression.Validate(condition, scope.allowed); err != nil {
				return result, err
			}
		}
	case "composite":
		for _, key := range []string{"main", "pre", "post", "pre-if", "post-if"} {
			if runs[key] != nil {
				return result, diagnostic(runs[key], "composite cannot declare %s", key)
			}
		}
		steps := runs["steps"]
		if steps == nil || steps.Kind != yaml.SequenceNode || len(steps.Content) == 0 || len(steps.Content) > 256 {
			return result, diagnostic(steps, "composite requires 1–256 steps")
		}
		seen := map[string]bool{}
		for i, n := range steps.Content {
			step, explicit, err := compileStep(n, i, scope)
			if err != nil {
				return result, err
			}
			if seen[step.ID] {
				return result, diagnostic(n, "duplicate composite step ID")
			}
			seen[step.ID] = true
			if explicit {
				scope.steps[step.ID] = true
			}
			result.Steps = append(result.Steps, step)
		}
	default:
		return result, diagnostic(runs["using"], "supported Action runtimes are node20, node24 and composite")
	}
	result.Outputs = map[string]string{}
	if n := fields["outputs"]; n != nil {
		outputs, err := mapping(n)
		if err != nil {
			return result, err
		}
		for key, entry := range outputs {
			if !identifier.MatchString(key) {
				return result, diagnostic(entry, "invalid output name")
			}
			f, err := mapping(entry, "description", "value")
			if err != nil {
				return result, err
			}
			if result.Using == "composite" {
				v, err := requiredString(f["value"])
				if err != nil {
					return result, err
				}
				if err := expression.ValidateTemplate(v, scope.allowed); err != nil {
					return result, err
				}
				result.Outputs[key] = v
			} else if f["value"] != nil {
				return result, diagnostic(f["value"], "JavaScript output cannot declare value")
			}
		}
	}
	return result, nil
}
