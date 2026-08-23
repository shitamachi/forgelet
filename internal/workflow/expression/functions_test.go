package expression

import (
	"errors"
	"testing"
)

func fnEnv() *Env {
	return NewEnv().
		With("github", githubCtx()).
		With("env", envCtx())
}

func TestStatusFunctions(t *testing.T) {
	fresh := fnEnv()
	cases := map[string][3]bool{ // success, failure, cancelled
		JobStatusSuccess:   {true, false, false},
		JobStatusFailure:   {false, true, false},
		JobStatusCancelled: {false, false, true},
	}
	for status, want := range cases {
		env := fresh.WithJobStatus(status)
		check := func(expr string, want bool) {
			got, err := Eval(expr, env)
			if err != nil || got.Kind != KindBool || got.Bool != want {
				t.Errorf("%s with status %s: got %s err=%v, want %v", expr, status, got.String(), err, want)
			}
		}
		check("success()", want[0])
		check("failure()", want[1])
		check("cancelled()", want[2])
		check("always()", true)
	}
	// Absent status defaults to success.
	for _, expr := range []string{"success()", "failure()", "cancelled()", "always()"} {
		if _, err := Eval(expr, fresh); err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
	}
}

func TestStringFunctions(t *testing.T) {
	env := fnEnv()
	cases := []struct {
		expr string
		want string
	}{
		{"contains('Hello World', 'LLO')", "true"},
		{"contains('abc', 'z')", "false"},
		{"contains(fromJSON('[\"a\",\"b\"]'), 'b')", "true"},
		{"startsWith('Hello', 'he')", "true"},
		{"startsWith('Hello', 'lo')", "false"},
		{"endsWith('Hello', 'LO')", "true"},
		{"format('Hello {0} {1}', 'world', 7)", "Hello world 7"},
		{"format('{{}}', 'x')", "{}"},
		{"format('no args')", "no args"},
		{"join(fromJSON('[1, \"a\", true]'), '-')", "1-a-true"},
		{"join('single', '-')", "single"},
		{"join(fromJSON('[\"x\",\"y\"]'))", "x,y"},
		{"toJSON(fromJSON('{\"a\":1}'))", `{"a":1}`},
		{"toJSON(3.5)", "3.5"},
		{"toJSON(3.0)", "3"},
		{"toJSON(null)", "null"},
		{"fromJSON('{\"k\": [1, true, \"s\"]}').k[1]", "true"},
		{"toJSON(github.event.commits)", `[{"message":"first"},{"message":"second"}]`},
		{"contains(github.sha, 'ABC')", "true"},
	}
	for _, tc := range cases {
		got, err := Eval(tc.expr, env)
		if err != nil {
			t.Errorf("%s: %v", tc.expr, err)
			continue
		}
		if got.String() != tc.want {
			t.Errorf("%s = %q, want %q", tc.expr, got.String(), tc.want)
		}
	}
}

func TestFunctionErrors(t *testing.T) {
	env := fnEnv()
	for _, expr := range []string{
		"contains('a')",
		"startsWith('a')",
		"format('x {5}', 1)",
		"format('broken {')",
		"format('stray }')",
		"toJSON()",
		"fromJSON('not json')",
		"hashFiles('**/*.go')", // T6: workspace capability not wired yet
		"unknownFn(1)",
	} {
		_, err := Eval(expr, env)
		if err == nil {
			t.Errorf("%q: expected error", expr)
			continue
		}
		if expr == "hashFiles('**/*.go')" {
			var nse *NotSupportedError
			if !errors.As(err, &nse) {
				t.Errorf("hashFiles: %T, want NotSupportedError", err)
			}
		}
	}
}

func TestInterpolate(t *testing.T) {
	env := fnEnv().WithJobStatus(JobStatusSuccess)
	cases := []struct {
		tpl  string
		want string
	}{
		{"plain text", "plain text"},
		{"${{ github.sha }}", "abc123"},
		{"a-${{ env.GO }}-b", "a-1.27-b"},
		{"${{ env.EMPTY || 'fb' }}${{ (1 == 1) || 'x' }}", "fbtrue"},
		{"no expressions at all", "no expressions at all"},
		{"${{ format('{0}/{1}', github.actor, env.GO) }}", "guo/1.27"},
		{"${{'quoted'}}", "quoted"},
	}
	for _, tc := range cases {
		got, err := Interpolate(tc.tpl, env)
		if err != nil {
			t.Errorf("%q: %v", tc.tpl, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%q = %q, want %q", tc.tpl, got, tc.want)
		}
	}

	if _, err := Interpolate("broken ${{ github.sha", env); err == nil {
		t.Error("unterminated placeholder must fail")
	}
	if _, err := Interpolate("${{ env.MISSING.x }}", NewEnv()); err == nil {
		t.Error("unavailable context in template must fail")
	}
	if !HasExpression("${{ x }}") || HasExpression("plain") {
		t.Error("HasExpression broken")
	}
}
