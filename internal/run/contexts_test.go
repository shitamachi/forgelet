package run

import (
	"encoding/json"
	"testing"
)

func TestPullRequestAndActionContexts(t *testing.T) {
	p := Plan{EventName: "pull_request", Ref: "refs/pull/12/merge", Repository: "me/repo", Event: json.RawMessage(`{"sender":{"login":"owner"},"pull_request":{"head":{"ref":"feature"}}}`), Inputs: map[string]any{"enabled": true}}
	values, typed := p.Contexts()
	if values["github.ref_name"] != "12/merge" || values["github.head_ref"] != "feature" || values["github.actor"] != "owner" || typed["inputs.enabled"] != true {
		t.Fatal(values, typed)
	}
	p.ActionInputs = map[string]string{"target": "prod"}
	values, typed = p.Contexts()
	raw, err := json.Marshal(typed["inputs"])
	if err != nil || string(raw) != `{"target":"prod"}` || values["inputs.target"] != "prod" || typed["inputs.enabled"] != nil {
		t.Fatal(values, typed, err)
	}
}
