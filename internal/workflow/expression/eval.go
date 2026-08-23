package expression

import (
	"strconv"
	"strings"
)

// Eval parses and evaluates one expression (the inner text of ${{ }}).
// Errors are typed: *ParseError, *ContextUnavailableError, *NotSupportedError,
// *EvalError. Errors are never silently converted to false (FR-E2.4).
func Eval(expr string, env *Env) (Value, error) {
	ast, err := parse(expr)
	if err != nil {
		return Null, err
	}
	if env == nil {
		env = NewEnv()
	}
	v, err := evalNode(ast, env, expr)
	if err != nil {
		return Null, err
	}
	return v, nil
}

func evalNode(n node, env *Env, expr string) (Value, error) {
	switch t := n.(type) {
	case *literalNode:
		return t.v, nil
	case *identNode:
		v, ok := env.Lookup(t.name)
		if !ok {
			return Null, &ContextUnavailableError{Want: t.name, Available: env.Available()}
		}
		return v, nil
	case *memberNode:
		return evalMember(t, env, expr)
	case *unaryNode:
		x, err := evalNode(t.x, env, expr)
		if err != nil {
			return Null, err
		}
		return BoolOf(!x.Truthy()), nil
	case *binaryNode:
		return evalBinary(t, env, expr)
	case *callNode:
		fn, ok := functions[strings.ToLower(t.name)]
		if !ok {
			return Null, &NotSupportedError{What: "function " + t.name + "()"}
		}
		args := make([]Value, 0, len(t.args))
		for _, a := range t.args {
			v, err := evalNode(a, env, expr)
			if err != nil {
				return Null, err
			}
			args = append(args, v)
		}
		return fn(env, args)
	default:
		return Null, &EvalError{Expr: expr, Msg: "unknown AST node"}
	}
}

func evalMember(m *memberNode, env *Env, expr string) (Value, error) {
	obj, err := evalNode(m.obj, env, expr)
	if err != nil {
		return Null, err
	}
	prop, err := evalNode(m.prop, env, expr)
	if err != nil {
		return Null, err
	}
	// GitHub semantics: property access on null or scalars yields null;
	// missing keys, out-of-range and negative indexes yield null.
	switch obj.Kind {
	case KindObject:
		if prop.Kind == KindString {
			return objKey(obj.Obj, prop.Str), nil
		}
		if prop.Kind == KindNull {
			return Null, nil
		}
		return Null, nil
	case KindArray:
		if prop.Kind == KindNumber {
			idx := int(prop.Num)
			if idx < 0 || idx >= len(obj.Arr) {
				return Null, nil
			}
			return obj.Arr[idx], nil
		}
		return Null, nil
	default:
		return Null, nil
	}
}

func objKey(m map[string]Value, key string) Value {
	if v, ok := m[key]; ok {
		return v
	}
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return Null
}

func evalBinary(b *binaryNode, env *Env, expr string) (Value, error) {
	switch b.op {
	case "&&":
		l, err := evalNode(b.l, env, expr)
		if err != nil {
			return Null, err
		}
		if !l.Truthy() {
			return l, nil
		}
		return evalNode(b.r, env, expr)
	case "||":
		l, err := evalNode(b.l, env, expr)
		if err != nil {
			return Null, err
		}
		if l.Truthy() {
			return l, nil
		}
		return evalNode(b.r, env, expr)
	}

	l, err := evalNode(b.l, env, expr)
	if err != nil {
		return Null, err
	}
	r, err := evalNode(b.r, env, expr)
	if err != nil {
		return Null, err
	}

	switch b.op {
	case "==":
		return BoolOf(looseEqual(l, r)), nil
	case "!=":
		return BoolOf(!looseEqual(l, r)), nil
	case "<", "<=", ">", ">=":
		cmp, ok := compare(l, r)
		if !ok {
			return BoolOf(false), nil
		}
		switch b.op {
		case "<":
			return BoolOf(cmp < 0), nil
		case "<=":
			return BoolOf(cmp <= 0), nil
		case ">":
			return BoolOf(cmp > 0), nil
		default:
			return BoolOf(cmp >= 0), nil
		}
	}
	return Null, &EvalError{Expr: expr, Msg: "unknown operator " + b.op}
}

// looseEqual implements GitHub equality:
//   - same kind: direct (strings case-insensitive);
//   - either side a number: numeric comparison (unparsable strings never equal);
//   - null equals the empty string;
//   - otherwise compare via the display string (case-insensitive).
func looseEqual(l, r Value) bool {
	if l.Kind == r.Kind {
		switch l.Kind {
		case KindNull:
			return true
		case KindBool:
			return l.Bool == r.Bool
		case KindNumber:
			return l.Num == r.Num
		case KindString:
			return strings.EqualFold(l.Str, r.Str)
		default:
			return l.String() == r.String()
		}
	}
	// null == '' (and by extension '' == null).
	if l.Kind == KindNull && r.Kind == KindString {
		return r.Str == ""
	}
	if r.Kind == KindNull && l.Kind == KindString {
		return l.Str == ""
	}
	// Numeric comparison when either side is a number.
	if l.Kind == KindNumber || r.Kind == KindNumber {
		ln, lok := toNumber(l)
		rn, rok := toNumber(r)
		if !lok || !rok {
			return false
		}
		return ln == rn
	}
	// Numbers in string form ("1" vs 1) already covered above; remaining
	// mismatches compare display strings case-insensitively.
	return strings.EqualFold(l.String(), r.String())
}

// toNumber coerces a value for numeric comparison. Booleans and numeric
// strings convert; objects/arrays/null do not.
func toNumber(v Value) (float64, bool) {
	switch v.Kind {
	case KindNumber:
		return v.Num, true
	case KindBool:
		if v.Bool {
			return 1, true
		}
		return 0, true
	case KindString:
		f, err := strconv.ParseFloat(strings.TrimSpace(v.Str), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

// compare returns -1/0/1; ok=false when the pair is not comparable
// (numeric attempt first, then case-insensitive strings).
func compare(l, r Value) (int, bool) {
	ln, lok := toNumber(l)
	rn, rok := toNumber(r)
	if lok && rok && (l.Kind == KindNumber || r.Kind == KindNumber) {
		switch {
		case ln < rn:
			return -1, true
		case ln > rn:
			return 1, true
		default:
			return 0, true
		}
	}
	if l.Kind == KindString && r.Kind == KindString {
		return strings.Compare(strings.ToLower(l.Str), strings.ToLower(r.Str)), true
	}
	return 0, false
}
