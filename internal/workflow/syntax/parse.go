package syntax

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Parse decodes and validates one workflow document. All problems are
// reported together; on any error the AST is discarded.
func Parse(filename string, data []byte) (*Workflow, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, &Error{Diagnostics: []Diagnostic{{
			File: filename, Line: 1, Column: 1, Path: "$",
			Message: fmt.Sprintf("invalid YAML: %v", err),
		}}}
	}
	root := unwrapDoc(&doc)
	p := &parser{file: filename}
	wf := p.parseWorkflow(root)
	if len(p.diags) > 0 {
		return nil, &Error{Diagnostics: p.diags}
	}
	return wf, nil
}

func unwrapDoc(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		return n.Content[0]
	}
	return n
}

type parser struct {
	file  string
	diags []Diagnostic
}

func (p *parser) fail(node *yaml.Node, path, msg string) {
	p.diags = append(p.diags, Diagnostic{
		File: p.file, Line: node.Line, Column: node.Column, Path: path, Message: msg,
	})
}

func pos(node *yaml.Node) Position { return Position{Line: node.Line, Column: node.Column} }

// mapping walks key/value pairs of a mapping node, rejecting unknown keys
// against the whitelist.
func (p *parser) mapping(node *yaml.Node, path string, allowed map[string]bool, visit func(key *yaml.Node, value *yaml.Node)) {
	if node.Kind != yaml.MappingNode {
		p.fail(node, path, fmt.Sprintf("expected mapping, got %s", kindName(node)))
		return
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		field := key.Value
		if !allowed[field] {
			p.fail(key, path, fmt.Sprintf("%q %s", field, subsetMessage))
			continue
		}
		visit(key, value)
	}
}

func kindName(n *yaml.Node) string {
	switch n.Kind {
	case yaml.MappingNode:
		return "mapping"
	case yaml.SequenceNode:
		return "sequence"
	case yaml.ScalarNode:
		return fmt.Sprintf("scalar (%q)", n.Value)
	default:
		return "alias/other"
	}
}

func (p *parser) parseWorkflow(root *yaml.Node) *Workflow {
	wf := &Workflow{File: p.file}
	p.mapping(root, "", map[string]bool{"name": true, "on": true, "jobs": true},
		func(key, value *yaml.Node) {
			switch key.Value {
			case "name":
				wf.Name = p.scalarString(value, ".name")
			case "on":
				p.parseOn(value, wf)
			case "jobs":
				p.parseJobs(value, wf)
			}
		})
	return wf
}

func (p *parser) parseOn(node *yaml.Node, wf *Workflow) {
	switch node.Kind {
	case yaml.ScalarNode: // on: push | on: pull_request
		switch node.Value {
		case "push":
			wf.On.Push = &PushTrigger{}
		case "pull_request":
			wf.On.PullRequest = &PushTrigger{}
		default:
			p.fail(node, ".on", fmt.Sprintf("%q %s", node.Value, subsetMessage))
		}
	case yaml.MappingNode:
		p.mapping(node, ".on", map[string]bool{"push": true, "pull_request": true, "schedule": true}, func(_, v *yaml.Node) {
			// The key node is needed to route; re-read from the mapping by
			// walking again is ugly — the closure loses it, so parse by kind.
		})
		// The mapping helper above only validates keys; parse values here.
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			switch key.Value {
			case "push":
				wf.On.Push = p.parsePush(value)
			case "pull_request":
				wf.On.PullRequest = p.parsePush(value)
			case "schedule":
				wf.On.Schedule = p.parseSchedule(value, ".on.schedule")
			}
		}
	default:
		p.fail(node, ".on", "expected a trigger name or a trigger mapping")
	}
}

// parseSchedule accepts `schedule: [{cron: "..."}` lists.
func (p *parser) parseSchedule(node *yaml.Node, path string) []string {
	if node.Kind != yaml.SequenceNode {
		p.fail(node, path, "expected sequence of {cron: expression}")
		return nil
	}
	var out []string
	for idx, item := range node.Content {
		ipath := fmt.Sprintf("%s[%d]", path, idx)
		if item.Kind != yaml.MappingNode {
			p.fail(item, ipath, "expected mapping with a cron key")
			continue
		}
		p.mapping(item, ipath, map[string]bool{"cron": true}, func(_, v *yaml.Node) {})
		for i := 0; i+1 < len(item.Content); i += 2 {
			if item.Content[i].Value == "cron" {
				out = append(out, p.scalarString(item.Content[i+1], ipath+".cron"))
			}
		}
	}
	return out
}

func (p *parser) parsePush(node *yaml.Node) *PushTrigger {
	if node.Kind != yaml.MappingNode {
		if node.Kind == yaml.ScalarNode && (node.Tag == "!!null" || node.Value == "") {
			return &PushTrigger{}
		}
		p.fail(node, ".on.push", "expected mapping of push filters")
		return nil
	}
	trigger := &PushTrigger{}
	p.mapping(node, ".on.push", map[string]bool{"branches": true, "branches-ignore": true},
		func(key, value *yaml.Node) {
			switch key.Value {
			case "branches":
				trigger.Branches = p.stringSeq(value, ".on.push.branches")
			case "branches-ignore":
				trigger.BranchesIgnore = p.stringSeq(value, ".on.push.branches-ignore")
			}
		})
	return trigger
}

func (p *parser) parseJobs(node *yaml.Node, wf *Workflow) {
	if node.Kind != yaml.MappingNode {
		p.fail(node, ".jobs", "expected mapping of job id to job")
		return
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		jobID := key.Value
		if !isIdentifier(jobID) {
			p.fail(key, ".jobs", fmt.Sprintf("job id %q must start with a letter or '_' and contain only alphanumerics, '-' or '_'", jobID))
			continue
		}
		job := p.parseJob(value, jobID)
		if job != nil {
			job.ID = jobID
			job.Pos = pos(key)
			wf.Jobs = append(wf.Jobs, job)
		}
	}
}

func (p *parser) parseJob(node *yaml.Node, jobID string) *Job {
	job := &Job{}
	path := ".jobs." + jobID
	p.mapping(node, path, map[string]bool{"name": true, "runs-on": true, "needs": true, "strategy": true, "env": true, "steps": true},
		func(key, value *yaml.Node) {
			switch key.Value {
			case "name":
				job.Name = p.scalarString(value, path+".name")
			case "runs-on":
				job.RunsOn = p.scalarString(value, path+".runs-on")
			case "needs":
				job.Needs = p.parseNeeds(value, path+".needs")
			case "strategy":
				job.Matrix = p.parseStrategy(value, path+".strategy")
			case "env":
				job.Env = p.envMap(value, path+".env")
			case "steps":
				job.Steps = p.parseSteps(value, jobID)
			}
		})
	return job
}

// parseNeeds accepts `needs: job` and `needs: [a, b]`.
func (p *parser) parseNeeds(node *yaml.Node, path string) []string {
	if node.Kind == yaml.ScalarNode {
		return []string{p.scalarString(node, path)}
	}
	return p.stringSeq(node, path)
}

// parseStrategy accepts only strategy.matrix {axis: [values]}; include and
// exclude are explicitly out of the declared subset (spec 0001 FR-2.4).
func (p *parser) parseStrategy(node *yaml.Node, path string) map[string][]string {
	if node.Kind != yaml.MappingNode {
		p.fail(node, path, "expected mapping with a matrix key")
		return nil
	}
	var matrix map[string][]string
	p.mapping(node, path, map[string]bool{"matrix": true}, func(_, v *yaml.Node) {
		if v.Kind != yaml.MappingNode {
			p.fail(v, path+".matrix", "expected mapping of axis name to value list")
			return
		}
		matrix = map[string][]string{}
		for i := 0; i+1 < len(v.Content); i += 2 {
			axis, values := v.Content[i], v.Content[i+1]
			if !isIdentifier(axis.Value) {
				p.fail(axis, path+".matrix", "axis names must be identifiers")
				continue
			}
			matrix[axis.Value] = p.stringSeq(values, path+".matrix."+axis.Value)
		}
	})
	return matrix
}

func (p *parser) parseSteps(node *yaml.Node, jobID string) []*Step {
	if node.Kind != yaml.SequenceNode {
		p.fail(node, ".jobs."+jobID+".steps", "expected sequence of steps")
		return nil
	}
	steps := make([]*Step, 0, len(node.Content))
	for idx, item := range node.Content {
		path := fmt.Sprintf(".jobs.%s.steps[%d]", jobID, idx)
		step := &Step{Pos: Position{Line: item.Line, Column: item.Column}}
		p.mapping(item, path, map[string]bool{"name": true, "run": true, "env": true},
			func(key, value *yaml.Node) {
				switch key.Value {
				case "name":
					step.Name = p.scalarString(value, path+".name")
				case "run":
					step.Run = p.scalarString(value, path+".run")
				case "env":
					step.Env = p.envMap(value, path+".env")
				}
			})
		steps = append(steps, step)
	}
	return steps
}

func (p *parser) scalarString(node *yaml.Node, path string) string {
	if node.Kind != yaml.ScalarNode {
		p.fail(node, path, fmt.Sprintf("expected string, got %s", kindName(node)))
		return ""
	}
	if node.Tag == "!!int" || node.Tag == "!!bool" || node.Tag == "!!float" {
		p.fail(node, path, fmt.Sprintf("expected string, got %s", kindName(node)))
		return ""
	}
	return node.Value
}

func (p *parser) stringSeq(node *yaml.Node, path string) []string {
	if node.Kind != yaml.SequenceNode {
		p.fail(node, path, "expected sequence of strings")
		return nil
	}
	out := make([]string, 0, len(node.Content))
	for _, item := range node.Content {
		out = append(out, p.scalarString(item, path))
	}
	return out
}

func (p *parser) envMap(node *yaml.Node, path string) map[string]string {
	if node.Kind != yaml.MappingNode {
		p.fail(node, path, "expected mapping of env name to string value")
		return nil
	}
	env := make(map[string]string, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		env[key.Value] = p.scalarString(value, path+"."+key.Value)
	}
	return env
}

func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9', r == '-':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// TrimRefPrefix strips refs/heads/ for branch matching.
func TrimRefPrefix(ref string) string {
	return strings.TrimPrefix(ref, "refs/heads/")
}
