package expression

import (
	"fmt"
	"strconv"
	"strings"
)

var functionArity = map[string][2]int{
	"success": {0, 0}, "failure": {0, 0}, "cancelled": {0, 0}, "always": {0, 0},
	"contains": {2, 2}, "startswith": {2, 2}, "endswith": {2, 2}, "format": {1, 32},
	"hashfiles": {1, 32}, "join": {1, 2}, "tojson": {1, 1}, "fromjson": {1, 1},
}

type value struct {
	kind   byte
	text   string
	number float64
	bool   bool
	object any
}

func boolean(v bool) value { return value{kind: 'b', bool: v} }

func fromAny(v any) value {
	switch v := v.(type) {
	case nil:
		return value{}
	case bool:
		return boolean(v)
	case string:
		return value{kind: 's', text: v}
	case float64:
		return value{kind: 'n', number: v}
	case int:
		return value{kind: 'n', number: float64(v)}
	default:
		return value{kind: 'j', object: v}
	}
}
func (v value) string() string {
	switch v.kind {
	case 's':
		return v.text
	case 'b':
		return strconv.FormatBool(v.bool)
	case 'n':
		return strconv.FormatFloat(v.number, 'g', -1, 64)
	}
	return ""
}

func String(v any) string { return fromAny(v).string() }

// Parts locates wrappers without interpreting closing braces inside quoted strings.
func Parts(source string, visit func(string) (string, error)) (string, error) {
	var out strings.Builder
	for {
		start := strings.Index(source, "${{")
		if start < 0 {
			out.WriteString(source)
			break
		}
		out.WriteString(source[:start])
		rest := source[start+3:]
		quoted := false
		end := -1
		for i := 0; i < len(rest); i++ {
			if rest[i] == '\'' {
				if quoted && i+1 < len(rest) && rest[i+1] == '\'' {
					i++
					continue
				}
				quoted = !quoted
			}
			if !quoted && i+1 < len(rest) && rest[i:i+2] == "}}" {
				end = i
				break
			}
		}
		if end < 0 {
			return "", fmt.Errorf("unterminated expression")
		}
		expr := strings.TrimSpace(rest[:end])
		if expr == "" {
			return "", fmt.Errorf("empty expression")
		}
		value, err := visit(expr)
		if err != nil {
			return "", err
		}
		out.WriteString(value)
		if out.Len() > 1<<20 {
			return "", fmt.Errorf("expanded value exceeds 1 MiB")
		}
		source = rest[end+2:]
	}
	if out.Len() > 1<<20 {
		return "", fmt.Errorf("expanded value exceeds 1 MiB")
	}
	return out.String(), nil
}
func Interpolate(source string, ctx Context) (string, error) {
	return Parts(source, func(expr string) (string, error) {
		v, err := Eval(expr, ctx)
		if err != nil {
			return "", err
		}
		if fromAny(v).kind == 'j' {
			return "", fmt.Errorf("object requires toJSON before interpolation")
		}
		return String(v), nil
	})
}
func ValidateTemplate(source string, allowed func(string) bool) error {
	_, err := Parts(source, func(expr string) (string, error) { return "", Validate(expr, allowed) })
	return err
}
