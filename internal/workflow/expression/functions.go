package expression

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Job status values consulted by success()/failure()/cancelled().
const (
	JobStatusSuccess   = "success"
	JobStatusFailure   = "failure"
	JobStatusCancelled = "cancelled"
)

// jobStatusContext is the internal Env entry registering the current job
// status for the status functions. Absent means "nothing failed yet".
const jobStatusContext = "__jobstatus"

// WithJobStatus registers the job status consulted by success(), failure()
// and cancelled(). always() ignores it.
func (e *Env) WithJobStatus(status string) *Env {
	return e.With(jobStatusContext, StrOf(status))
}

func (e *Env) jobStatus() string {
	if v, ok := e.Lookup(jobStatusContext); ok && v.Kind == KindString {
		return v.Str
	}
	return JobStatusSuccess
}

// builtin functions (spec 0007 FR-3.3). hashFiles evaluates through the
// injected workspace capability (hashfiles.go).
var functions = map[string]func(env *Env, args []Value) (Value, error){
	"hashfiles": hashFilesFn,
	"success": func(env *Env, _ []Value) (Value, error) {
		return BoolOf(env.jobStatus() == JobStatusSuccess), nil
	},
	"failure": func(env *Env, _ []Value) (Value, error) {
		return BoolOf(env.jobStatus() == JobStatusFailure), nil
	},
	"cancelled": func(env *Env, _ []Value) (Value, error) {
		return BoolOf(env.jobStatus() == JobStatusCancelled), nil
	},
	"always": func(_ *Env, _ []Value) (Value, error) {
		return BoolOf(true), nil
	},
	"contains": func(_ *Env, args []Value) (Value, error) {
		if err := arity("contains", args, 2); err != nil {
			return Null, err
		}
		if args[0].Kind == KindArray {
			for _, item := range args[0].Arr {
				if looseEqual(item, args[1]) {
					return BoolOf(true), nil
				}
			}
			return BoolOf(false), nil
		}
		return BoolOf(strings.Contains(
			strings.ToLower(args[0].String()),
			strings.ToLower(args[1].String()))), nil
	},
	"startswith": func(_ *Env, args []Value) (Value, error) {
		if err := arity("startsWith", args, 2); err != nil {
			return Null, err
		}
		return BoolOf(strings.HasPrefix(strings.ToLower(args[0].String()), strings.ToLower(args[1].String()))), nil
	},
	"endswith": func(_ *Env, args []Value) (Value, error) {
		if err := arity("endsWith", args, 2); err != nil {
			return Null, err
		}
		return BoolOf(strings.HasSuffix(strings.ToLower(args[0].String()), strings.ToLower(args[1].String()))), nil
	},
	"format": func(_ *Env, args []Value) (Value, error) {
		if len(args) == 0 {
			return Null, &EvalError{Msg: "format() needs a template"}
		}
		return formatGitHub(args[0].String(), args[1:])
	},
	"join": func(_ *Env, args []Value) (Value, error) {
		if err := arity("join", args, 1, 2); err != nil {
			return Null, err
		}
		sep := ","
		if len(args) == 2 {
			sep = args[1].String()
		}
		if args[0].Kind != KindArray {
			return StrOf(args[0].String()), nil
		}
		parts := make([]string, 0, len(args[0].Arr))
		for _, v := range args[0].Arr {
			parts = append(parts, v.String())
		}
		return StrOf(strings.Join(parts, sep)), nil
	},
	"tojson": func(_ *Env, args []Value) (Value, error) {
		if err := arity("toJSON", args, 1); err != nil {
			return Null, err
		}
		b, err := json.Marshal(valueToAny(args[0]))
		if err != nil {
			return Null, &EvalError{Msg: "toJSON: " + err.Error()}
		}
		return StrOf(string(b)), nil
	},
	"fromjson": func(_ *Env, args []Value) (Value, error) {
		if err := arity("fromJSON", args, 1); err != nil {
			return Null, err
		}
		var raw any
		dec := json.NewDecoder(strings.NewReader(args[0].String()))
		dec.UseNumber()
		if err := dec.Decode(&raw); err != nil {
			return Null, &EvalError{Msg: "fromJSON: " + err.Error()}
		}
		return anyToValue(raw), nil
	},
}

func arity(name string, args []Value, want ...int) error {
	for _, w := range want {
		if len(args) == w {
			return nil
		}
	}
	return &EvalError{Msg: fmt.Sprintf("%s() takes %d argument(s), got %d", name, want[0], len(args))}
}

// formatGitHub replaces {0}, {1}, ... with args; doubled braces are literal.
func formatGitHub(tpl string, args []Value) (Value, error) {
	var sb strings.Builder
	for i := 0; i < len(tpl); i++ {
		switch tpl[i] {
		case '{':
			if strings.HasPrefix(tpl[i:], "{{") {
				sb.WriteByte('{')
				i++
				continue
			}
			end := strings.IndexByte(tpl[i:], '}')
			if end < 0 {
				return Null, &EvalError{Msg: "format(): unterminated { in template"}
			}
			idx, err := strconv.Atoi(tpl[i+1 : i+end])
			if err != nil || idx < 0 || idx >= len(args) {
				return Null, &EvalError{Msg: fmt.Sprintf("format(): bad placeholder %q", tpl[i:i+end+1])}
			}
			sb.WriteString(args[idx].String())
			i += end
		case '}':
			if strings.HasPrefix(tpl[i:], "}}") {
				sb.WriteByte('}')
				i++
				continue
			}
			return Null, &EvalError{Msg: "format(): stray } in template"}
		default:
			sb.WriteByte(tpl[i])
		}
	}
	return StrOf(sb.String()), nil
}

func valueToAny(v Value) any {
	switch v.Kind {
	case KindNull:
		return nil
	case KindBool:
		return v.Bool
	case KindNumber:
		return json.Number(FormatNumber(v.Num))
	case KindString:
		return v.Str
	case KindArray:
		out := make([]any, 0, len(v.Arr))
		for _, item := range v.Arr {
			out = append(out, valueToAny(item))
		}
		return out
	case KindObject:
		out := make(map[string]any, len(v.Obj))
		for k, item := range v.Obj {
			out[k] = valueToAny(item)
		}
		return out
	default:
		return nil
	}
}

func anyToValue(raw any) Value {
	switch t := raw.(type) {
	case nil:
		return Null
	case bool:
		return BoolOf(t)
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return StrOf(t.String())
		}
		return NumOf(f)
	case float64:
		return NumOf(t)
	case string:
		return StrOf(t)
	case []any:
		arr := make([]Value, 0, len(t))
		for _, item := range t {
			arr = append(arr, anyToValue(item))
		}
		return ArrOf(arr...)
	case map[string]any:
		obj := make(map[string]Value, len(t))
		for k, item := range t {
			obj[k] = anyToValue(item)
		}
		return ObjOf(obj)
	default:
		return Null
	}
}
