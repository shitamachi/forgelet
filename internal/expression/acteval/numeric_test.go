package acteval

import (
	"math"
	"testing"
)

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
	if IsTruthy(math.NaN()) {
		t.Fatal("NaN must be falsy")
	}
}
