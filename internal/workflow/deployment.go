package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"sort"
	"strings"

	"github.com/shitamachi/forgelet/internal/action"
	"github.com/shitamachi/forgelet/internal/run"
)

// DeploymentBinding is administrator policy, separate from repository YAML.
type DeploymentBinding struct {
	Target, Job, Profile string
}

// AttachDeployments adds ordinary native jobs to the persisted graph. Every
// original job must succeed, and the selected publisher must be unambiguous.
// Repository source, secrets and action inputs are never rewritten.
func AttachDeployments(plans []run.Plan, bindings []DeploymentBinding) ([]run.Plan, error) {
	if len(bindings) == 0 {
		return plans, nil
	}
	if len(plans) == 0 || len(plans)+len(bindings) > 256 {
		return nil, fmt.Errorf("automatic deployment exceeds workflow graph limits")
	}
	out := append([]run.Plan(nil), plans...)
	keys := make(map[string]bool, len(plans))
	needs := make([]string, 0, len(plans))
	groups := map[string]bool{}
	for _, p := range plans {
		keys[p.Key] = true
		group := p.Group
		if group == "" {
			group = p.Key
		}
		if !groups[group] {
			needs = append(needs, group)
			groups[group] = true
		}
	}
	sort.Strings(needs)
	for _, b := range bindings {
		if b.Target == "" || b.Job == "" || b.Profile == "" {
			return nil, fmt.Errorf("automatic deployment requires target, publisher job and native profile")
		}
		key := "forgelet_deploy_" + strings.ReplaceAll(b.Target, "-", "_")
		if !identifier.MatchString(key) || keys[key] {
			return nil, fmt.Errorf("automatic deployment job %q conflicts with workflow", key)
		}
		publisher, step := -1, ""
		for i, p := range out[:len(plans)] {
			if p.Key != b.Job {
				continue
			}
			if publisher >= 0 || p.Control != "" || p.MatrixTotal > 1 {
				return nil, fmt.Errorf("automatic deployment publisher %q must be one ordinary job", b.Job)
			}
			publisher = i
			for _, s := range p.Steps {
				if s.Action != nil && (s.Action.Repository == "docker/build-push-action" || s.Action.Uses == "forgelet/build@v1") {
					if step != "" {
						return nil, fmt.Errorf("automatic deployment publisher %q has multiple image build steps", b.Job)
					}
					step = s.ID
				}
			}
		}
		if publisher < 0 || step == "" {
			return nil, fmt.Errorf("automatic deployment publisher %q requires one docker/build-push-action or forgelet/build step", b.Job)
		}
		p := &out[publisher]
		if len(p.Outputs) >= 100 {
			return nil, fmt.Errorf("automatic deployment publisher output limit exceeded")
		}
		output := key + "_digest"
		if _, exists := p.Outputs[output]; exists {
			return nil, fmt.Errorf("automatic deployment output %q conflicts with workflow", output)
		}
		p.Outputs = maps.Clone(p.Outputs)
		if p.Outputs == nil {
			p.Outputs = map[string]string{}
		}
		p.Outputs[output] = fmt.Sprintf("${{ steps['%s'].outputs.digest }}", step)
		deploy, err := action.Reference("forgelet/deploy@v1")
		if err != nil {
			return nil, fmt.Errorf("deployment action: %w", err)
		}
		deploy.Inputs = map[string]string{"target": b.Target, "digest": fmt.Sprintf("${{ needs['%s'].outputs['%s'] }}", p.Key, output)}
		sum := sha256.Sum256([]byte(p.RunID + "\x00" + key))
		out = append(out, run.Plan{
			JobID: hex.EncodeToString(sum[:])[:24], RunID: p.RunID, Key: key, Group: key, Name: "Deploy " + b.Target,
			Repository: p.Repository, SHA: p.SHA, Ref: p.Ref, EventName: p.EventName, BaseRef: p.BaseRef,
			WorkflowName: p.WorkflowName, SourceWorkflow: p.SourceWorkflow, Event: append([]byte(nil), p.Event...),
			WorkflowConcurrency: p.WorkflowConcurrency, WorkflowCancelInProgress: p.WorkflowCancelInProgress,
			Profile: b.Profile, Permissions: map[string]string{"contents": "none"}, Needs: append([]string(nil), needs...),
			TimeoutSeconds: p.TimeoutSeconds, Steps: []run.Step{{ID: "deploy", Name: "Deliver image digest", Action: deploy}},
		})
		keys[key] = true
	}
	return out, nil
}
