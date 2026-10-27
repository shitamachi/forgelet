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

func TestReusableJobContextRetainsLocalJobAndOriginalEvent(t *testing.T) {
	p := Plan{Group: "outer/inner/emit", EventName: "workflow_dispatch", Repository: "me/repo", Event: json.RawMessage(`{"inputs":{"label":"from-event"}}`)}
	values, typed := p.Contexts()
	if values["github.job"] != "emit" || values["github.event_name"] != "workflow_dispatch" || typed["github.event"].(map[string]any)["inputs"] == nil {
		t.Fatal(values, typed)
	}
}

func TestRefTypeAndTokenEnvironment(t *testing.T) {
	for _, tc := range []struct{ ref, kind, name string }{{"refs/heads/main", "branch", "main"}, {"refs/tags/v1.2.3", "tag", "v1.2.3"}, {"refs/pull/4/merge", "branch", "4/merge"}} {
		p := Plan{Repository: "o/r", Ref: tc.ref}
		if tc.kind == "branch" && tc.name == "4/merge" {
			p.EventName = "pull_request"
		}
		values, _ := p.Contexts()
		if values["github.ref_type"] != tc.kind || values["github.ref_name"] != tc.name {
			t.Fatal(values)
		}
	}
	for _, tc := range []struct {
		name, value string
		want        bool
	}{{"GITHUB_TOKEN", "${{ secrets.GITHUB_TOKEN }}", true}, {"GITHUB_TOKEN", "${{secrets.GITHUB_TOKEN}}", true}, {"GITHUB_TOKEN", "secrets.GITHUB_TOKEN", false}, {"GITHUB_SHA", "${{ secrets.GITHUB_TOKEN }}", false}, {"GITHUB_TOKEN", "literal", false}} {
		if TokenEnvironment(tc.name, tc.value) != tc.want {
			t.Fatal(tc)
		}
	}
}
