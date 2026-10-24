package expression

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

var functionArity = map[string][2]int{
	"success": {0, 0}, "failure": {0, 0}, "cancelled": {0, 0}, "always": {0, 0},
	"contains": {2, 2}, "startswith": {2, 2}, "endswith": {2, 2}, "format": {1, 32},
	"join": {1, 2}, "tojson": {1, 1}, "fromjson": {1, 1},
}

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
func (v value) any() any {
	switch v.kind {
	case 's':
		return v.text
	case 'b':
		return v.bool
	case 'n':
		return v.number
	case 'j':
		return v.object
	}
	return nil
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

// Eval evaluates a value without the implicit condition-only success gate.
func Eval(source string, ctx Context) (any, error) {
	n, err := parse(source)
	if err != nil {
		return nil, err
	}
	if n == nil {
		return nil, nil
	}
	v := n.evaluate(ctx)
	return v.any(), v.err
}
func String(v any) string { return fromAny(v).string() }

func (n *node) call(ctx Context) value {
	args := make([]value, len(n.args))
	for i, arg := range n.args {
		args[i] = arg.evaluate(ctx)
		if args[i].err != nil {
			return args[i]
		}
	}
	fail := func(message string) value { return value{err: fmt.Errorf("%s: %s", n.op, message)} }
	switch n.op {
	case "contains":
		if list, ok := args[0].object.([]any); ok {
			for _, v := range list {
				if compare(fromAny(v), args[1], "==") {
					return boolean(true)
				}
			}
			return boolean(false)
		}
		return boolean(strings.Contains(strings.ToLower(args[0].string()), strings.ToLower(args[1].string())))
	case "startswith":
		return boolean(strings.HasPrefix(strings.ToLower(args[0].string()), strings.ToLower(args[1].string())))
	case "endswith":
		return boolean(strings.HasSuffix(strings.ToLower(args[0].string()), strings.ToLower(args[1].string())))
	case "tojson":
		data, err := json.Marshal(args[0].any())
		if err != nil {
			return fail("cannot encode value")
		}
		if len(data) > 64<<10 {
			return fail("result exceeds 64 KiB")
		}
		return fromAny(string(data))
	case "fromjson":
		var v any
		if err := json.Unmarshal([]byte(args[0].string()), &v); err != nil {
			return fail("invalid JSON")
		}
		return fromAny(v)
	case "join":
		separator := ","
		if len(args) == 2 {
			separator = args[1].string()
		}
		list, ok := args[0].object.([]any)
		if !ok {
			return fromAny(args[0].string())
		}
		values := make([]string, len(list))
		for i, v := range list {
			values[i] = String(v)
		}
		result := strings.Join(values, separator)
		if len(result) > 64<<10 {
			return fail("result exceeds 64 KiB")
		}
		return fromAny(result)
	case "format":
		source := args[0].string()
		var result strings.Builder
		for i := 0; i < len(source); {
			c := source[i]
			if c != '{' && c != '}' {
				result.WriteByte(c)
				i++
				continue
			}
			if i+1 < len(source) && source[i+1] == c {
				result.WriteByte(c)
				i += 2
				continue
			}
			if c == '}' {
				return fail("unmatched brace")
			}
			end := strings.IndexByte(source[i+1:], '}')
			if end < 0 {
				return fail("unmatched brace")
			}
			index, err := strconv.Atoi(source[i+1 : i+1+end])
			if err != nil || index < 0 || index+1 >= len(args) {
				return fail("invalid argument index")
			}
			result.WriteString(args[index+1].string())
			i += end + 2
			if result.Len() > 64<<10 {
				return fail("result exceeds 64 KiB")
			}
		}
		return fromAny(result.String())
	}
	return fail("unsupported function")
}

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
