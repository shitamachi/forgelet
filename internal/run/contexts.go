package run

import (
	"encoding/json"
	"strings"
)

// Contexts contains public, already-fixed plan inputs. RuntimeSecrets is populated
// only in the executor and deliberately has no JSON representation.
func (p Plan) Contexts() (map[string]string, map[string]any) {
	eventName := p.EventName
	if eventName == "" {
		eventName = "push"
	}
	values := map[string]string{"github.workflow": p.WorkflowName, "github.job": p.Group, "github.run_id": p.RunID, "github.repository_owner": strings.SplitN(p.Repository, "/", 2)[0], "github.server_url": "https://github.com", "github.api_url": "https://api.github.com", "github.sha": p.SHA, "github.ref": p.Ref, "github.ref_name": strings.TrimPrefix(strings.TrimPrefix(p.Ref, "refs/heads/"), "refs/tags/"), "github.repository": p.Repository, "github.event_name": eventName, "github.base_ref": p.BaseRef, "github.action_path": p.ActionPath}
	var event struct {
		Sender struct {
			Login string `json:"login"`
		} `json:"sender"`
		PullRequest struct {
			Head struct {
				Ref string `json:"ref"`
			} `json:"head"`
		} `json:"pull_request"`
	}
	_ = json.Unmarshal(p.Event, &event)
	values["github.actor"] = event.Sender.Login
	values["github.head_ref"] = event.PullRequest.Head.Ref
	if eventName == "pull_request" {
		values["github.ref_name"] = strings.TrimPrefix(p.Ref, "refs/pull/")
	}
	typed := map[string]any{"matrix": p.Matrix, "inputs": p.Inputs, "strategy.job-index": p.MatrixIndex, "strategy.job-total": p.MatrixTotal, "strategy.max-parallel": p.MaxParallel, "strategy.fail-fast": p.FailFast}
	for key, v := range p.Matrix {
		typed["matrix."+key] = v
	}
	if p.ActionInputs == nil {
		for key, v := range p.Inputs {
			typed["inputs."+key] = v
		}
	}
	for key, v := range p.Vars {
		values["vars."+key] = v
	}
	for key, v := range p.RuntimeSecrets {
		values["secrets."+key] = v
	}
	for key, v := range p.ActionInputs {
		values["inputs."+key] = v
		typed["inputs."+key] = v
	}
	if p.ActionInputs != nil {
		typed["inputs"] = p.ActionInputs
	}
	return values, typed
}
