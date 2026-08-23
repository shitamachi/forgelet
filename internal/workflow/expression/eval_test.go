package expression

import (
	"errors"
	"testing"
)

func githubCtx() Value {
	return ObjOf(map[string]Value{
		"sha":   StrOf("abc123"),
		"ref":   StrOf("refs/heads/main"),
		"actor": StrOf("guo"),
		"event": ObjOf(map[string]Value{
			"name":   StrOf("push"),
			"after":  StrOf("abc123"),
			"pusher": ObjOf(map[string]Value{"name": StrOf("guo")}),
			"commits": ArrOf(
				ObjOf(map[string]Value{"message": StrOf("first")}),
				ObjOf(map[string]Value{"message": StrOf("second")}),
			),
		}),
	})
}

func envCtx() Value {
	return ObjOf(map[string]Value{
		"GO":    StrOf("1.27"),
		"EMPTY": StrOf(""),
	})
}

func fullEnv() *Env {
	return NewEnv().With("github", githubCtx()).With("env", envCtx())
}

func TestEvalOperators(t *testing.T) {
	env := fullEnv()
	cases := []struct {
		expr string
		want any // bool / string / float64 / raw Value
	}{
		// literals
		{"null", KindNull},
		{"true", true},
		{"false", false},
		{"42", 42.0},
		{"3.25", 3.25},
		{"'hi'", "hi"},
		{"'it''s'", "it's"},
		// equality
		{"1 == 1", true},
		{"1 == 2", false},
		{"1 == '1'", true},
		{"'abc' == 'abc'", true},
		{"'ABC' == 'abc'", true},
		{"null == ''", true},
		{"'' == null", true},
		{"null == null", true},
		{"null == 'x'", false},
		{"null == 0", false},
		{"'abc' == true", false},
		{"true == 'true'", true},
		{"false == 'False'", true},
		{"1 != 2", true},
		{"github.sha == 'abc123'", true},
		{"github.sha == 'ABC123'", true},
		// comparison
		{"1 < 2", true},
		{"2 <= 2", true},
		{"3 > 4", false},
		{"4 >= 5", false},
		{"'a' < 'b'", true},
		{"'B' < 'a'", false}, // case-insensitive: 'b' > 'a'
		{"'A' < 'b'", true},  // case-insensitive: 'a' < 'b'
		{"'abc' < 3", false},
		{"10 > '9'", true}, // numeric coercion when either side is a number
		// boolean algebra returns operands
		{"true && 'x'", "x"},
		{"false && 'x'", false},
		{"'a' || 'b'", "a"},
		{"false || 'b'", "b"},
		{"null || 'z'", "z"},
		{"!true", false},
		{"!null", true},
		{"!''", true},
		{"!0", true},
		{"!'x'", false},
		{"!(1 == 1)", false},
		{"!!'x'", true},
		// precedence and associativity
		{"true || false && false", true},
		{"(true || false) && false", false},
		{"1 < 2 && 2 < 3", true},
		{"1 == 1 == true", true}, // left associative: (1==1)==true
		{"!1 == 0", true},        // (!1) == 0 → false... see below
		{"true == !false", true},
		// truthiness of numbers
		{"0 || 'fallback'", "fallback"},
		{"1 && 'yes'", "yes"},
	}
	for _, tc := range cases {
		got, err := Eval(tc.expr, env)
		if err != nil {
			t.Errorf("%s: error %v", tc.expr, err)
			continue
		}
		assertValue(t, tc.expr, got, tc.want)
	}
}

func assertValue(t *testing.T, expr string, got Value, want any) {
	t.Helper()
	switch w := want.(type) {
	case bool:
		if got.Kind != KindBool || got.Bool != w {
			t.Errorf("%s = %s, want bool %v", expr, got.String(), w)
		}
	case string:
		if got.Kind != KindString || got.Str != w {
			t.Errorf("%s = %s, want string %q", expr, got.String(), w)
		}
	case float64:
		if got.Kind != KindNumber || got.Num != w {
			t.Errorf("%s = %s, want number %v", expr, got.String(), w)
		}
	case Kind:
		// Used for Null expectations.
		if got.Kind != w {
			t.Errorf("%s = kind %d, want %d", expr, got.Kind, w)
		}
	}
}

func TestEvalContextAccess(t *testing.T) {
	env := fullEnv()
	cases := []struct {
		expr string
		want string
	}{
		{"github.sha", "abc123"},
		{"github.ref", "refs/heads/main"},
		{"github.event.name", "push"},
		{"github.event.pusher.name", "guo"},
		{"github.event.commits[0].message", "first"},
		{"github.event.commits[1].message", "second"},
		{"github.event.commits[10].message", ""}, // out of range -> null
		{"github.event.commits[-1].message", ""}, // negative -> null
		{"github['sha']", "abc123"},
		{"github.event['pusher']['name']", "guo"},
		{"github.EVENT.name", "push"}, // keys case-insensitive
		{"GITHUB.SHA", "abc123"},      // context name case-insensitive
		{"env.GO", "1.27"},
		{"env.go", "1.27"},
		{"github.missing.deep.chain", ""},       // missing -> null -> .deep -> null
		{"env.EMPTY || 'fallback'", "fallback"}, // empty string falsy
	}
	for _, tc := range cases {
		got, err := Eval(tc.expr, env)
		if err != nil {
			t.Errorf("%s: error %v", tc.expr, err)
			continue
		}
		if got.String() != tc.want {
			t.Errorf("%s = %q, want %q", tc.expr, got.String(), tc.want)
		}
	}
}

func TestEvalUnavailableContextTyped(t *testing.T) {
	// Scheduler-time phase: only github registered.
	schedulerEnv := NewEnv().With("github", githubCtx())
	_, err := Eval("env.GO == '1.27'", schedulerEnv)
	if err == nil {
		t.Fatal("env access in scheduler phase must fail")
	}
	var cue *ContextUnavailableError
	if !errors.As(err, &cue) {
		t.Fatalf("error type %T, want ContextUnavailableError", err)
	}
	if cue.Want != "env" {
		t.Errorf("want context = %q", cue.Want)
	}
	if len(cue.Available) != 1 || cue.Available[0] != "github" {
		t.Errorf("available = %v, want [github]", cue.Available)
	}

	// Runtime phase with env registered: same expression succeeds.
	if _, err := Eval("env.GO == '1.27'", fullEnv()); err != nil {
		t.Fatalf("runtime phase: %v", err)
	}
}

func TestEvalTwoPhaseConsistency(t *testing.T) {
	// Expressions valid in both phases evaluate identically (FR-E2.3 / AC 3).
	exprs := []string{
		"github.sha == 'abc123'",
		"github.event.commits[0].message == 'first'",
		"!github.event.name != 'push'",
		"(github.actor == 'guo' || github.actor == 'bot') && 1 < 2",
	}
	schedulerEnv := NewEnv().With("github", githubCtx())
	runtimeEnv := schedulerEnv.With("env", envCtx())
	for _, expr := range exprs {
		a, aerr := Eval(expr, schedulerEnv)
		b, berr := Eval(expr, runtimeEnv)
		if aerr != nil || berr != nil {
			t.Errorf("%s: %v / %v", expr, aerr, berr)
			continue
		}
		if a.Kind != b.Kind || a.String() != b.String() {
			t.Errorf("%s: phases disagree: %s vs %s", expr, a.String(), b.String())
		}
	}
}

func TestEvalParseErrors(t *testing.T) {
	env := fullEnv()
	cases := []struct {
		expr    string
		wantMsg string
	}{
		{"", "end of expression"},
		{"1 +", "arithmetic"},
		{"1 + 2", "arithmetic"},
		{"2 * 3", "arithmetic"},
		{`"double"`, "double-quoted"},
		{"a.*.b", "arithmetic"}, // '*' rejected by lexer
		{"true &&", "unexpected"},
		{"(1 == 1", "expected ')'"},
		{"github[0", "expected ']'"},
		{"github.", "expected property name"},
		{"'unterminated", "unterminated"},
		{"1 2", "unexpected"},
		{"== 1", "unexpected"},
	}
	for _, tc := range cases {
		_, err := Eval(tc.expr, env)
		if err == nil {
			t.Errorf("%q: expected parse error", tc.expr)
			continue
		}
		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Errorf("%q: error %T (%v), want ParseError", tc.expr, err, err)
			continue
		}
		if pe.Line < 1 || pe.Column < 1 {
			t.Errorf("%q: position %d:%d must be positive", tc.expr, pe.Line, pe.Column)
		}
	}
}

func TestEvalFunctionNotSupported(t *testing.T) {
	for _, expr := range []string{"success()", "contains('a', 'b')", "format('{0}', 1)"} {
		_, err := Eval(expr, fullEnv())
		var nse *NotSupportedError
		if !errors.As(err, &nse) {
			t.Errorf("%q: error %T (%v), want NotSupportedError", expr, err, err)
		}
	}
	// Malformed call still parse-errors.
	if _, err := Eval("format('x'", fullEnv()); err == nil {
		t.Error("malformed call must fail")
	}
}

func TestEvalNeverSilentlyFalse(t *testing.T) {
	// Unknown context is an error, not false (FR-E2.4).
	if _, err := Eval("matrix.go == '1.27'", fullEnv()); err == nil {
		t.Fatal("unknown context must error")
	}
}

func TestValueString(t *testing.T) {
	if got := NumOf(1.0).String(); got != "1" {
		t.Errorf("1.0 renders %q", got)
	}
	if got := NumOf(3.25).String(); got != "3.25" {
		t.Errorf("3.25 renders %q", got)
	}
	if got := Null.String(); got != "" {
		t.Errorf("null renders %q", got)
	}
	if got := ArrOf(StrOf("a"), NumOf(2)).String(); got != "a,2" {
		t.Errorf("array renders %q", got)
	}
}

func TestEnvImmutableAndCaseInsensitive(t *testing.T) {
	base := NewEnv().With("github", githubCtx())
	extended := base.With("Env", envCtx())
	if _, ok := base.Lookup("env"); ok {
		t.Error("base env was mutated by With")
	}
	if v, ok := extended.Lookup("ENV"); !ok || v.Kind != KindObject || v.Obj["GO"].Str != "1.27" {
		t.Errorf("case-insensitive lookup failed: %+v %v", v, ok)
	}
	if got := extended.Available(); len(got) != 2 {
		t.Errorf("available = %v", got)
	}
}
