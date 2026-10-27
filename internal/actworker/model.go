// Package actworker adapts one resolved forgelet job to the pinned act library.
// It is linked only into the isolated worker binary, not the control plane.
package actworker

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/nektos/act/pkg/model"
	"gopkg.in/yaml.v3"

	"github.com/shitamachi/forgelet/internal/action"
	"github.com/shitamachi/forgelet/internal/run"
)

// Freeze only already-resolved plan contexts. Action metadata keeps its own
// inputs context; secrets and step outputs remain runtime expressions.
func freeze(value string, p run.Plan) string {
	objects := map[string]any{"inputs": p.Inputs, "strategy": map[string]any{"job-index": p.MatrixIndex, "job-total": p.MatrixTotal, "max-parallel": p.MaxParallel, "fail-fast": p.FailFast}}
	for name, obj := range objects {
		data, _ := json.Marshal(obj)
		if obj == nil {
			data = []byte("{}")
		}
		replacement := "fromJSON('" + strings.ReplaceAll(string(data), "'", "''") + "')"
		var out strings.Builder
		for i := 0; i < len(value); {
			start := strings.Index(value[i:], "${{")
			if start < 0 {
				out.WriteString(value[i:])
				break
			}
			start += i
			out.WriteString(value[i : start+3])
			i = start + 3
			quoted := false
			for i < len(value) {
				if value[i] == '\'' {
					out.WriteByte(value[i])
					i++
					if quoted && i < len(value) && value[i] == '\'' {
						out.WriteByte(value[i])
						i++
						continue
					}
					quoted = !quoted
					continue
				}
				if !quoted && strings.HasPrefix(value[i:], "}}") {
					out.WriteString("}}")
					i += 2
					break
				}
				if !quoted && strings.HasPrefix(value[i:], name) && (i == 0 || !word(value[i-1]) && value[i-1] != '.') && (i+len(name) == len(value) || !word(value[i+len(name)])) {
					out.WriteString(replacement)
					i += len(name)
					continue
				}
				out.WriteByte(value[i])
				i++
			}
		}
		value = out.String()
	}
	return value
}
func word(b byte) bool {
	return unicode.IsLetter(rune(b)) || unicode.IsDigit(rune(b)) || b == '_' || b == '-'
}
func frozenMap(values map[string]string, p run.Plan) map[string]string {
	out := map[string]string{}
	for k, v := range values {
		out[k] = freeze(v, p)
	}
	return out
}
func expression(value string) string {
	if value == "" {
		return ""
	}
	if strings.Contains(value, "${{") {
		return value
	}
	return "${{ " + value + " }}"
}

func workflow(p run.Plan) (*model.Workflow, error) {
	job := &model.Job{Name: p.Name, Outputs: frozenMap(p.Outputs, p), If: yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "always()"}}
	_ = job.Env.Encode(frozenMap(p.Env, p))
	_ = job.RawRunsOn.Encode([]string{"forgelet"})
	wf := &model.Workflow{Name: p.WorkflowName, File: p.SourceWorkflow, Jobs: map[string]*model.Job{localKey(p): job}}
	var needs []string
	for key, dep := range p.NeedsContext {
		if key == p.ParentGate {
			continue
		}
		if local := p.NeedsAliases[key]; local != "" {
			key = local
		}
		needs = append(needs, key)
		wf.Jobs[key] = &model.Job{Result: string(dep.Result), Outputs: dep.Outputs}
	}
	_ = job.RawNeeds.Encode(needs)
	for _, s := range p.Steps {
		step := &model.Step{ID: s.ID, Name: s.Name, Run: freeze(s.Run, p), Shell: s.Shell, WorkingDirectory: freeze(s.WorkingDirectory, p), If: yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: freeze(expression(s.If), p)}}
		_ = step.Env.Encode(frozenMap(s.Env, p))
		if s.TimeoutSeconds > 0 {
			step.TimeoutMinutes = fmt.Sprintf("%g", float64(s.TimeoutSeconds)/60)
		}
		if s.ContinueOnErrorIf != "" {
			step.RawContinueOnError = freeze(s.ContinueOnErrorIf, p)
		} else if s.ContinueOnError {
			step.RawContinueOnError = "true"
		}
		if s.Checkout != nil {
			sha := p.ActionPins["actions/checkout@v4"]
			if sha == "" {
				if len(p.ActionPins) > 0 {
					return nil, fmt.Errorf("checkout is absent from the saved Action lock")
				}
				sha = action.ActVersions["actions/checkout@v4"]
			}
			step.Uses = "actions/checkout@" + sha
			step.With = map[string]string{"repository": p.Repository, "ref": p.SHA, "persist-credentials": "false", "fetch-depth": fmt.Sprint(s.Checkout.FetchDepth), "path": s.Checkout.Path}
		} else if s.Action != nil {
			step.Uses = s.Action.Uses
			if s.Action.Repository != "" {
				fixed, err := action.RemoteReference(s.Action.Repository + "@" + s.Action.SHA)
				if err != nil || fixed.SHA == "" || len(p.ActionPins) > 0 && p.ActionPins[s.Action.Repository+"@"+s.Action.Ref] != s.Action.SHA {
					return nil, fmt.Errorf("remote Action must have a saved full commit SHA")
				}
				step.Uses = s.Action.Repository
				if s.Action.Path != "" {
					step.Uses += "/" + s.Action.Path
				}
				step.Uses += "@" + s.Action.SHA
			}
			step.With = frozenMap(s.Action.Inputs, p)
		}
		job.Steps = append(job.Steps, step)
	}
	return wf, nil
}

func localKey(p run.Plan) string {
	key := p.Group
	if key == "" {
		key = p.Key
	}
	if at := strings.LastIndexByte(key, '/'); at >= 0 {
		key = key[at+1:]
	}
	return key
}
