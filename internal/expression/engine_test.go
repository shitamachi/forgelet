package expression

import (
	"strings"
	"testing"
)

func TestEngineContextsIndexingFilteringAndCoercion(t *testing.T) {
	ctx := Context{Success: true, Failure: true, Cancelled: true, Values: map[string]string{"env.LOOP": "env.LOOP", "secrets.API_KEY": "value", "needs.build.result": "success"}, Typed: map[string]any{"github.event": map[string]any{"pull_request": map[string]any{"labels": []any{map[string]any{"name": "bug"}, map[string]any{"name": "ready"}}}}, "inputs.index": float64(1)}}
	for _, tc := range []struct {
		source string
		want   any
	}{
		{"contains(github.event.pull_request.labels.*.name, 'READY')", true},
		{"fromJSON('[10,20]')[inputs.index]", float64(20)},
		{"secrets['API_KEY']", "value"},
		{"needs['build'].result", "success"},
		{"env.LOOP > 0", false},
		{"failure() && cancelled() && success()", true},
		{"2 > 1 == true", true}, {"(2 > (1 == true))", true},
		{"3 < 2 < 1", true}, {"null <= null", true},
	} {
		got, err := Eval(tc.source, ctx)
		if err != nil || got != tc.want {
			t.Errorf("%s = %#v %v want %#v", tc.source, got, err, tc.want)
		}
	}
	for source, want := range map[string]string{"secrets.API_KEY": "secrets.API_KEY", "secrets['API_KEY']": "secrets.API_KEY", "needs['build'].outputs.VALUE": "needs.build.outputs.VALUE"} {
		seen := ""
		if err := Validate(source, func(ref string) bool { seen = ref; return true }); err != nil || seen != want {
			t.Fatal(source, seen, err)
		}
	}
}
func TestEngineBoundsAndNoImplicitIO(t *testing.T) {
	ctx := Context{Success: true, Values: map[string]string{"env.LARGE": strings.Repeat("x", 64<<10)}}
	for _, source := range []string{"format('{0}{0}', env.LARGE)", "hashFiles('**')", "format('{-1}', 'x')"} {
		if _, err := Eval(source, ctx); err == nil {
			t.Fatalf("accepted %s", source)
		}
	}
}
