package expression

import (
	"math"
	"strings"
	"testing"
)

func TestEvaluate(t *testing.T) {
	ctx := Context{Success: true, Values: map[string]string{
		"github.ref": "refs/heads/main", "needs.build.result": "success",
		"needs.build.outputs.version": "42", "env.EMPTY": "", "env.FALSE": "false",
		"steps.test.outcome": "failure", "steps.test.conclusion": "success",
	}}
	tests := []struct {
		source string
		want   bool
	}{
		{"", true}, {"  \n\t", true}, {"${{ true }}", true}, {"false", false}, {"null", false},
		{"0", false}, {"-0", false}, {"-2", true}, {"0.0", false}, {"0x10 == 16", true}, {"2.5e2 == 250", true},
		{"''", false}, {"'false'", true}, {"'It''s fine' == 'it''s FINE'", true},
		{"github.ref == 'REFS/HEADS/MAIN'", true}, {"needs.build.result != 'failure'", true},
		{"needs.build.outputs.version == 42", true}, {"needs.build.outputs.version > 40", true},
		{"env.MISSING == ''", true}, {"env.MISSING == 0", true}, {"env.FALSE", true},
		{"env.EMPTY != 0", false}, {"'abc' == 0", false}, {"'abc' != 0", true},
		{"'abc' < 1", false}, {"'abc' >= 1", false}, {"'42' == '042'", false},
		{"'  42 ' == 42", true}, {"true == 1", true}, {"false == 0", true}, {"null == 0", true},
		{"null == null", true}, {"null < null", false}, {"null >= null", true},
		{"false < true", true}, {"true > false", true}, {"true <= true", true},
		{"'a' < 'B'", true}, {"'z' > 'Y'", true}, {"'0xff' == 255", true}, {"'0o10' == 8", true},
		{"'Infinity' > 5", true}, {"'-Infinity' < 0", true}, {"'garbage' == false", false},
		{"!false && true", true}, {"true || false && false", true}, {"(true || false) && false", false},
		{"!(true && false)", true}, {"1 < 2 == true", true}, {"1 == true == true", true},
		{"(true && 'yes' || 'no') == 'yes'", true}, {"(false && 'yes' || 'no') == 'no'", true},
		{"('' || needs.build.outputs.version) == 42", true}, {"('x' && '') == ''", true},
		{"steps.test.outcome == 'failure' && steps.test.conclusion == 'success'", true},
		{"success() && !failure() && !cancelled() && always()", true}, {"ALWAYS()", true},
	}
	for _, tc := range tests {
		t.Run(tc.source, func(t *testing.T) {
			got, err := Evaluate(tc.source, ctx)
			if err != nil || got != tc.want {
				t.Fatalf("Evaluate(%q) = %v, %v; want %v", tc.source, got, err, tc.want)
			}
		})
	}
}

func TestDefaultSuccessGateAndStatusOverrides(t *testing.T) {
	failed := Context{Failure: true}
	cancelled := Context{Cancelled: true}
	tests := []struct {
		source string
		ctx    Context
		want   bool
	}{
		{"", failed, false}, {"true", failed, false}, {"1 == 1", failed, false},
		{"always()", failed, true}, {"failure()", failed, true}, {"success()", failed, false},
		{"true || failure()", failed, true}, // Syntactic presence, even in the unevaluated branch, removes the default gate.
		{"'always()'", failed, false},       // A string containing a function name is not a status function.
		{"!cancelled()", failed, true}, {"!cancelled()", cancelled, false},
		{"cancelled()", cancelled, true}, {"always() && false", cancelled, false},
		{"false || failure()", cancelled, false},
	}
	for _, tc := range tests {
		t.Run(tc.source, func(t *testing.T) {
			got, err := Evaluate(tc.source, tc.ctx)
			if err != nil || got != tc.want {
				t.Fatalf("Evaluate(%q) = %v, %v; want %v", tc.source, got, err, tc.want)
			}
		})
	}
}

func TestLogicalOperatorsShortCircuit(t *testing.T) {
	// An invalid internal right node would panic if evaluated. Public parsing
	// always validates both operands before evaluating either branch.
	poison := &node{op: "!"}
	for _, tc := range []struct {
		op   string
		left bool
	}{{"&&", false}, {"||", true}} {
		root := &node{op: tc.op, left: &node{op: "literal", literal: boolean(tc.left)}, right: poison}
		if got := root.evaluate(Context{}).truthy(); got != tc.left {
			t.Fatalf("short circuit %s returned %v", tc.op, got)
		}
	}
}

func TestValidateAllBranches(t *testing.T) {
	allowed := func(name string) bool { return name == "github.ref" }
	for _, source := range []string{"github.ref == 'main'", "always()", "", "false && github.ref"} {
		if err := Validate(source, allowed); err != nil {
			t.Errorf("Validate(%q): %v", source, err)
		}
	}
	for _, source := range []string{"false && secrets.TOKEN", "true || needs.undeclared.result", "secrets.TOKEN || true"} {
		if err := Validate(source, allowed); err == nil || !strings.Contains(err.Error(), "unsupported context reference") {
			t.Errorf("Validate(%q) must inspect skipped branches, got %v", source, err)
		}
	}
	if err := Validate("needs.anything.outputs.value", nil); err != nil {
		t.Fatal(err)
	}
}

func TestRejectInvalidAndUnboundedConditions(t *testing.T) {
	for _, source := range []string{
		"${{ }}", "${{ true", "${{ true }} trailing", "true }}", "'unclosed", `"double quoted"`,
		"true &&", "true || ()", "!", "(", "(true", "true)", "true false", "true = false", "true & false", "true | false",
		"unknown('a', 'b')", "false && unknown()", "always(true)", "success(", "true == == false",
		"env['A']", "github", "steps.*.outcome", "env.A + 1", "1.2.3", "--1", ".", "0xnope",
		strings.Repeat("!", maxDepth+2) + "true", strings.Repeat("(", maxDepth+2) + "true" + strings.Repeat(")", maxDepth+2),
		"true" + strings.Repeat(" || true", maxDepth+2), "'" + strings.Repeat("x", maxBytes) + "'",
		strings.Repeat("()", maxTokens+1),
	} {
		t.Run(source[:min(len(source), 80)], func(t *testing.T) {
			if _, err := Evaluate(source, Context{Success: true}); err == nil {
				t.Errorf("Evaluate(%q) accepted invalid condition", source)
			}
			if err := Validate(source, nil); err == nil {
				t.Errorf("Validate(%q) accepted invalid condition", source)
			}
		})
	}
}

func TestNumericCoercion(t *testing.T) {
	for _, tc := range []struct {
		text string
		want float64
	}{
		{"", 0}, {"\t  ", 0}, {"+1", 1}, {".5", .5}, {"1.", 1}, {"1E+3", 1000},
		{"Infinity", math.Inf(1)}, {"+Infinity", math.Inf(1)}, {"-Infinity", math.Inf(-1)},
		{"1e9999", math.Inf(1)}, {"0xffffffff", -1}, {"0o37777777777", -1},
		{"true", math.NaN()}, {"NaN", math.NaN()}, {"0x100000000", math.NaN()}, {"0o8", math.NaN()},
	} {
		got := parseNumber(tc.text)
		if !(math.IsNaN(got) && math.IsNaN(tc.want)) && got != tc.want {
			t.Errorf("parseNumber(%q) = %v; want %v", tc.text, got, tc.want)
		}
	}
	if (value{kind: 'n', number: math.NaN()}).truthy() {
		t.Fatal("NaN must be falsy")
	}
}

func FuzzConditionDoesNotPanic(f *testing.F) {
	for _, source := range []string{"", "${{ true }}", "!", "true &&", "success(", "()", "github.ref == 'main'", "'a''b'", "false && secrets.TOKEN"} {
		f.Add(source)
	}
	f.Fuzz(func(t *testing.T, source string) {
		_, _ = Evaluate(source, Context{Success: true})
		_ = Validate(source, func(string) bool { return false })
	})
}
