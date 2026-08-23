package expression

import (
	"fmt"
	"strings"
)

// Interpolate expands every `${{ expr }}` in a template with the evaluated
// value (rendered like GitHub displays it). Unterminated placeholders are
// errors; text outside placeholders is preserved verbatim (spec 0007 T7).
func Interpolate(tpl string, env *Env) (string, error) {
	var sb strings.Builder
	for i := 0; i < len(tpl); i++ {
		if !strings.HasPrefix(tpl[i:], "${{") {
			sb.WriteByte(tpl[i])
			continue
		}
		end := strings.Index(tpl[i:], "}}")
		if end < 0 {
			return "", fmt.Errorf("expression: unterminated ${{ in template: %s", tpl)
		}
		inner := strings.TrimSpace(tpl[i+3 : i+end])
		v, err := Eval(inner, env)
		if err != nil {
			return "", fmt.Errorf("expression: template %q: %w", inner, err)
		}
		sb.WriteString(v.String())
		i += end + 1
	}
	return sb.String(), nil
}

// HasExpression reports whether the template contains any `${{` placeholder.
func HasExpression(s string) bool { return strings.Contains(s, "${{") }
