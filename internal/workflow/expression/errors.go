package expression

import (
	"fmt"
	"sort"
	"strings"
)

// ParseError reports syntax problems with a byte offset and 1-based
// line/column.
type ParseError struct {
	Expr   string
	Offset int
	Line   int
	Column int
	Msg    string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("expression parse error at %d:%d: %s", e.Line, e.Column, e.Msg)
}

// ContextUnavailableError reports access to a context that the current
// evaluation phase did not register (spec FR-E2.3). Available lists the
// registered context names.
type ContextUnavailableError struct {
	Want      string
	Available []string
}

func (e *ContextUnavailableError) Error() string {
	sort.Strings(e.Available)
	return fmt.Sprintf("expression: context %q is not available in this phase (available: %s)",
		e.Want, strings.Join(e.Available, ", "))
}

// NotSupportedError reports constructs that parse but are not supported in
// this build (functions until spec 0007 T5).
type NotSupportedError struct {
	What string
}

func (e *NotSupportedError) Error() string {
	return fmt.Sprintf("expression: %s is not supported in this build", e.What)
}

// EvalError wraps other evaluation failures with expression position info.
type EvalError struct {
	Expr string
	Msg  string
}

func (e *EvalError) Error() string {
	return fmt.Sprintf("expression eval error in %q: %s", e.Expr, e.Msg)
}
