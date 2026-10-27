package acteval

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

var decimal = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

func parseNumber(text string) float64 {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	if text == "Infinity" || text == "+Infinity" {
		return math.Inf(1)
	}
	if text == "-Infinity" {
		return math.Inf(-1)
	}
	if decimal.MatchString(text) {
		parsed, err := strconv.ParseFloat(text, 64)
		if err == nil || math.IsInf(parsed, 0) {
			return parsed
		}
	}
	if strings.HasPrefix(text, "0x") || strings.HasPrefix(text, "0o") {
		base := 16
		if text[1] == 'o' {
			base = 8
		}
		parsed, err := strconv.ParseUint(text[2:], base, 32)
		if err == nil {
			return float64(int32(parsed))
		}
	}
	return math.NaN()
}
