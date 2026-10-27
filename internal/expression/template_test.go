package expression

import "testing"

func TestFunctionsAndTypedInterpolation(t *testing.T) {
	ctx := Context{Typed: map[string]any{"matrix.experimental": false, "inputs.count": float64(3)}, Success: true}
	for _, tc := range []struct{ source, want string }{
		{"${{ matrix.experimental || 'fallback' }}", "fallback"},
		{"${{ format('{0}: {{ok}}', inputs.count) }}", "3: {ok}"},
		{"${{ join(fromJSON('[1,true,\"x\"]'), ':') }}", "1:true:x"},
		{"${{ contains(fromJSON('[\"Main\",\"dev\"]'), 'MAIN') }}", "true"},
		{"${{ startsWith('Main', 'ma') && endsWith('Main', 'IN') }}", "true"},
		{"${{ toJSON(fromJSON('{\"a\":false}')) }}", "{\n  \"a\": false\n}"},
		{"${{ format('}}{0}', 'done') }}", "}done"},
	} {
		got, err := Interpolate(tc.source, ctx)
		if err != nil || got != tc.want {
			t.Errorf("%s => %q,%v want %q", tc.source, got, err, tc.want)
		}
	}
	for _, source := range []string{"fromJSON('invalid')", "format('{1}', 'one')", "contains('one')", "join()", "format('{')"} {
		if _, err := Eval(source, ctx); err == nil {
			t.Errorf("accepted %s", source)
		}
	}
	if got, err := Eval("true || fromJSON('bad')", ctx); err != nil || got != true {
		t.Fatal(got, err)
	}
}

func TestWholeInputAndMatrixContexts(t *testing.T) {
	ctx := Context{Typed: map[string]any{"inputs": map[string]string{"value": "second"}, "matrix": map[string]any{"version": "1.25.0", "experimental": false}}}
	for source, want := range map[string]string{
		"${{ toJSON(inputs) }}": "{\n  \"value\": \"second\"\n}",
		"${{ toJSON(matrix) }}": "{\n  \"experimental\": false,\n  \"version\": \"1.25.0\"\n}",
	} {
		if err := ValidateTemplate(source, func(ref string) bool { return ref == "inputs" || ref == "matrix" }); err != nil {
			t.Fatal(err)
		}
		got, err := Interpolate(source, ctx)
		if err != nil || got != want {
			t.Fatal(got, err)
		}
	}
	for _, source := range []string{"${{ inputs }}", "${{ matrix }}"} {
		if _, err := Interpolate(source, ctx); err == nil {
			t.Fatal("accepted unsupported direct object interpolation", source)
		}
	}
}
