package server

import (
	"context"
	"maps"
	"strings"
	"testing"

	"github.com/shitamachi/forgelet/internal/kube"
	"github.com/shitamachi/forgelet/internal/run"
)

type imageProfileRuntime struct {
	*fakeRuntime
	selected map[string]string
}

func (r *imageProfileRuntime) Ensure(ctx context.Context, j run.Job, p kube.Profile) (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	r.selected = maps.Clone(p.Act.ImageMirrors)
	return r.fakeRuntime.Ensure(ctx, j, p)
}
func TestWorkflowImageGroupsSnapshotLegacyEmptyAndDispatch(t *testing.T) {
	ref := "registry.test/runner@sha256:" + strings.Repeat("a", 64)
	for _, mode := range []string{"selected", "legacy", "empty", "missing"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, 1)
			profile := h.cfg.Profiles["small"]
			profile.Act = &kube.ActProfile{RunnerImage: ref, ImageMirrors: map[string]string{"postgres:18-alpine": ref, "node:24-alpine": ref}, ImageGroups: map[string][]string{"pg": {"postgres:18-alpine"}, "none": {}}}
			h.cfg.Profiles["small"] = profile
			h.http.Close()
			h.start()
			m := h.server.management()
			m.Workflows = []WorkflowConfig{{Repository: "me/repo", Path: ".github/workflows/manual.yml", Enabled: true}}
			switch mode {
			case "selected":
				m.Workflows[0].ImageGroups = []string{"pg"}
			case "empty":
				m.Workflows[0].ImageGroups = []string{"none"}
			case "missing":
				m.Workflows[0].ImageGroups = []string{"unknown"}
			}
			h.server.managed.Store(&m)
			if mode == "missing" {
				code, _ := h.admin("POST", "/api/runs", map[string]any{"repository": "me/repo", "sha": testSHA, "workflow": testWorkflow})
				if code != 422 {
					t.Fatal("missing group admitted", code)
				}
				return
			}
			item := h.manual(testWorkflow)
			plan := item.Jobs[0].Plan
			if plan.ImageMirrorsSelected != (mode != "legacy") {
				t.Fatal("legacy selection changed", plan)
			}
			want := 0
			if mode == "selected" {
				want = 1
			}
			if len(plan.ImageMirrors) != want {
				t.Fatal("incorrect selection", plan.ImageMirrors)
			}
			// Selection is persisted; later management edits cannot silently change it.
			m.Workflows[0].ImageGroups = []string{"none"}
			runtime := &imageProfileRuntime{fakeRuntime: h.runtime}
			h.server.runtime = runtime
			h.tick()
			expected := want
			if mode == "legacy" {
				expected = 2
			}
			if len(runtime.selected) != expected {
				t.Fatal("dispatch used full/changed catalog", runtime.selected)
			}
			if len(h.server.cfg.Profiles["small"].Act.ImageMirrors) != 2 {
				t.Fatal("dispatch mutated shared profile")
			}
			profile.Act.ImageMirrors["postgres:18-alpine"] = "caller-mutated"
			if h.server.cfg.Profiles["small"].Act.ImageMirrors["postgres:18-alpine"] != ref {
				t.Fatal("caller mutated config")
			}
			if mode == "selected" {
				copy := h.server.cfg.Profiles["small"]
				delete(copy.Act.ImageMirrors, "postgres:18-alpine")
				if err := h.server.validateProfiles([]run.Plan{plan}); err == nil {
					t.Fatal("removed alias accepted for rerun")
				}
			}
		})
	}
}
