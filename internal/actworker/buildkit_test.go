package actworker

import (
	"testing"

	"github.com/nektos/act/pkg/model"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestBuildkitProfileDefaultsDoNotReplaceExplicitWorkflow(t *testing.T) {
	for _, tc := range []struct {
		name          string
		with          map[string]string
		image, config string
	}{
		{"default", nil, "mirror@sha256:pinned", "[registry]"},
		{"preserve explicit", map[string]string{"driver-opts": "network=host\nimage=explicit", "buildkitd-config": "local.toml"}, "network=host\nimage=explicit", ""},
		{"another driver", map[string]string{"driver": "remote"}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			step := &model.Step{Uses: "docker/setup-buildx-action@fixed", With: tc.with}
			wf := &model.Workflow{Jobs: map[string]*model.Job{"job": {Steps: []*model.Step{step}}}}
			configureBuilderDefaults(wf, run.ActRequest{BuildkitImage: "mirror@sha256:pinned", BuildkitConfig: "[registry]"})
			want := tc.image
			if tc.name == "default" {
				want = "image=" + want
			}
			if step.With["driver-opts"] != want || step.With["buildkitd-config-inline"] != tc.config {
				t.Fatal(step.With)
			}
		})
	}
}
