// Package expression implements the GitHub Actions-compatible expression
// engine shared by the control plane (scheduler-time) and the executor
// (runtime). It is pure: no clock, randomness, I/O or network, and no
// dependencies outside the standard library (spec 0001 FR-3.4).
package expression

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Kind tags a Value.
type Kind uint8

const (
	KindNull Kind = iota
	KindBool
	KindNumber
	KindString
	KindArray
	KindObject
)

// Value is a tagged union. Objects keep their keys verbatim but are looked
// up case-insensitively (GitHub semantics).
type Value struct {
	Kind Kind
	Bool bool
	Num  float64
	Str  string
	Arr  []Value
	Obj  map[string]Value
}

// Null is the null value.
var Null = Value{Kind: KindNull}

// BoolOf builds a boolean value.
func BoolOf(b bool) Value { return Value{Kind: KindBool, Bool: b} }

// NumOf builds a number value.
func NumOf(f float64) Value { return Value{Kind: KindNumber, Num: f} }

// StrOf builds a string value.
func StrOf(s string) Value { return Value{Kind: KindString, Str: s} }

// ArrOf builds an array value.
func ArrOf(vs ...Value) Value { return Value{Kind: KindArray, Arr: vs} }

// ObjOf builds an object value.
func ObjOf(m map[string]Value) Value {
	if m == nil {
		return Null
	}
	return Value{Kind: KindObject, Obj: m}
}

// Truthy implements GitHub truthiness: false, null, empty string, 0 and
// NaN are falsy.
func (v Value) Truthy() bool {
	switch v.Kind {
	case KindNull:
		return false
	case KindBool:
		return v.Bool
	case KindNumber:
		return v.Num != 0 && !math.IsNaN(v.Num)
	case KindString:
		return v.Str != ""
	case KindArray, KindObject:
		return true
	default:
		return false
	}
}

// String renders the value the way GitHub displays it: numbers without
// trailing decimals, booleans lowercase, null as empty.
func (v Value) String() string {
	switch v.Kind {
	case KindNull:
		return ""
	case KindBool:
		if v.Bool {
			return "true"
		}
		return "false"
	case KindNumber:
		return FormatNumber(v.Num)
	case KindString:
		return v.Str
	case KindArray:
		parts := make([]string, 0, len(v.Arr))
		for _, item := range v.Arr {
			parts = append(parts, item.String())
		}
		return strings.Join(parts, ",")
	case KindObject:
		keys := make([]string, 0, len(v.Obj))
		for k := range v.Obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%s", k, v.Obj[k].String()))
		}
		return strings.Join(parts, ",")
	default:
		return ""
	}
}

// FormatNumber renders integral numbers without a decimal point.
func FormatNumber(f float64) string {
	if math.IsNaN(f) {
		return "NaN"
	}
	if math.IsInf(f, 0) {
		return "Infinity"
	}
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}
