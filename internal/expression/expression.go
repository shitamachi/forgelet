// Package expression evaluates forgelet's bounded subset of GitHub Actions conditions.
// It has no I/O and deliberately rejects syntax outside the supported subset.
package expression

import (
	"fmt"

	"math"
	"regexp"
	"strconv"
	"strings"
)

// Context contains flattened string contexts and the caller's execution status.
// Missing string properties evaluate to the empty string, as in GitHub Actions.
type Context struct {
	Values                      map[string]string
	Typed                       map[string]any
	Success, Failure, Cancelled bool
}

// Evaluate applies the default success() gate unless an explicit status function
// occurs anywhere in the condition. Logical operators short-circuit and retain
// their operand values until the final truthiness conversion.
func Evaluate(source string, ctx Context) (bool, error) {
	root, err := parse(source)
	if err != nil {
		return false, err
	}
	if root == nil {
		return ctx.Success, nil
	}
	if !root.status && !ctx.Success {
		return false, nil
	}
	v := root.evaluate(ctx)
	return v.truthy(), v.err
}

// Validate checks every branch, including those which would short-circuit at
// runtime. A nil allowed callback validates syntax without restricting references.
func Validate(source string, allowed func(string) bool) error {
	root, err := parse(source)
	if err != nil || root == nil {
		return err
	}
	return root.validate(allowed)
}

const (
	maxBytes  = 8192
	maxTokens = 1024
	maxDepth  = 64
)

type value struct {
	kind   byte
	text   string
	number float64
	bool   bool
	object any
	err    error
}

func boolean(v bool) value { return value{kind: 'b', bool: v} }

func (v value) truthy() bool {
	switch v.kind {
	case 'b':
		return v.bool
	case 'n':
		return v.number != 0 && !math.IsNaN(v.number)
	case 's':
		return v.text != ""
	case 'j':
		return true
	default:
		return false
	}
}

func (v value) numeric() float64 {
	switch v.kind {
	case 'j':
		return math.NaN()
	case 'n':
		return v.number
	case 's':
		return parseNumber(v.text)
	case 'b':
		if v.bool {
			return 1
		}
	}
	return 0
}

// Numeric coercion follows the runner's ExpressionUtility.ParseNumber: whitespace
// and empty strings, signed decimal/exponent, hex/octal, and Infinity are explicit.
// https://github.com/actions/runner/blob/main/src/Sdk/DTExpressions2/Expressions2/Sdk/ExpressionUtility.cs
var decimal = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)
var reference = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*(?:\.[A-Za-z_][A-Za-z0-9_-]*)*$`)

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

type node struct {
	op          string
	literal     value
	left, right *node
	depth       int
	status      bool
	args        []*node
}

func (n *node) validate(allowed func(string) bool) error {
	if n.op == "reference" && allowed != nil && !allowed(n.literal.text) {
		return fmt.Errorf("unsupported context reference %q", n.literal.text)
	}
	for _, arg := range n.args {
		if err := arg.validate(allowed); err != nil {
			return err
		}
	}
	if n.left != nil {
		if err := n.left.validate(allowed); err != nil {
			return err
		}
	}
	if n.right != nil {
		return n.right.validate(allowed)
	}
	return nil
}

func (n *node) evaluate(ctx Context) value {
	switch n.op {
	case "literal":
		return n.literal
	case "reference":
		if v, ok := ctx.Typed[n.literal.text]; ok {
			return fromAny(v)
		}
		return value{kind: 's', text: ctx.Values[n.literal.text]}
	case "success":
		return boolean(ctx.Success)
	case "failure":
		return boolean(ctx.Failure)
	case "cancelled":
		return boolean(ctx.Cancelled)
	case "always":
		return boolean(true)
	case "!":
		v := n.left.evaluate(ctx)
		if v.err != nil {
			return v
		}
		return boolean(!v.truthy())
	case "&&":
		left := n.left.evaluate(ctx)
		if left.err != nil {
			return left
		}
		if !left.truthy() {
			return left
		}
		return n.right.evaluate(ctx)
	case "||":
		left := n.left.evaluate(ctx)
		if left.err != nil {
			return left
		}
		if left.truthy() {
			return left
		}
		return n.right.evaluate(ctx)
	default:
		if _, ok := functionArity[n.op]; ok {
			return n.call(ctx)
		}
		l, r := n.left.evaluate(ctx), n.right.evaluate(ctx)
		if l.err != nil {
			return l
		}
		if r.err != nil {
			return r
		}
		return boolean(compare(l, r, n.op))
	}
}

func compare(left, right value, op string) bool {
	if left.kind != right.kind {
		left = value{kind: 'n', number: left.numeric()}
		right = value{kind: 'n', number: right.numeric()}
	}
	equal, less, greater := false, false, false
	switch left.kind {
	case 's':
		l, r := strings.ToUpper(left.text), strings.ToUpper(right.text)
		equal, less, greater = l == r, l < r, l > r
	case 'n':
		equal, less, greater = left.number == right.number, left.number < right.number, left.number > right.number
	case 'j':
		equal = false
	case 'b':
		equal, less, greater = left.bool == right.bool, !left.bool && right.bool, left.bool && !right.bool
	default:
		equal = true // null == null; strict ordering between nulls is false.
	}
	switch op {
	case "==":
		return equal
	case "!=":
		return !equal
	case "<":
		return less
	case "<=":
		return less || equal
	case ">":
		return greater
	case ">=":
		return greater || equal
	}
	return false
}

type token struct {
	kind  byte
	text  string
	value value
	pos   int
}

type parser struct {
	tokens []token
	index  int
}

func parse(source string) (*node, error) {
	if len(source) > maxBytes {
		return nil, fmt.Errorf("condition exceeds %d bytes", maxBytes)
	}
	source = strings.TrimSpace(source)
	if strings.HasPrefix(source, "${{") {
		if !strings.HasSuffix(source, "}}") {
			return nil, fmt.Errorf("unterminated condition wrapper")
		}
		source = strings.TrimSpace(source[3 : len(source)-2])
		if source == "" {
			return nil, fmt.Errorf("condition wrapper is empty")
		}
	}
	if source == "" {
		return nil, nil
	}
	tokens, err := lex(source)
	if err != nil {
		return nil, err
	}
	p := parser{tokens: tokens}
	root, err := p.expression(1, 0)
	if err != nil {
		return nil, err
	}
	if p.peek().kind != 'e' {
		return nil, fmt.Errorf("unexpected condition token at offset %d", p.peek().pos)
	}
	return root, nil
}

func (p *parser) peek() token { return p.tokens[p.index] }

func (p *parser) expression(minimum, depth int) (*node, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("condition exceeds maximum depth %d", maxDepth)
	}
	current := p.peek()
	p.index++
	var left *node
	switch {
	case current.kind == 'v':
		left = &node{op: "literal", literal: current.value, depth: 1}
	case current.kind == 'r':
		if p.peek().text == "(" {
			name := strings.ToLower(current.text)
			limits, ok := functionArity[name]
			if !ok {
				return nil, fmt.Errorf("unsupported condition function %q", current.text)
			}
			p.index++
			left = &node{op: name, depth: 1, status: name == "success" || name == "failure" || name == "cancelled" || name == "always"}
			if p.peek().text != ")" {
				for {
					arg, err := p.expression(1, depth+1)
					if err != nil {
						return nil, err
					}
					left.args = append(left.args, arg)
					left.depth = max(left.depth, arg.depth+1)
					left.status = left.status || arg.status
					if p.peek().text != "," {
						break
					}
					p.index++
				}
			}
			if p.peek().text != ")" {
				return nil, fmt.Errorf("missing function closing parenthesis")
			}
			p.index++
			if len(left.args) < limits[0] || len(left.args) > limits[1] {
				return nil, fmt.Errorf("invalid argument count for %s", name)
			}

		} else {
			if !strings.Contains(current.text, ".") && current.text != "inputs" && current.text != "matrix" {
				return nil, fmt.Errorf("unsupported condition identifier %q", current.text)
			}
			left = &node{op: "reference", literal: value{text: current.text}, depth: 1}
		}
	case current.text == "!":
		child, err := p.expression(5, depth+1)
		if err != nil {
			return nil, err
		}
		left = &node{op: "!", left: child, status: child.status, depth: child.depth + 1}
	case current.text == "(":
		child, err := p.expression(1, depth+1)
		if err != nil {
			return nil, err
		}
		if p.peek().text != ")" {
			return nil, fmt.Errorf("missing condition closing parenthesis")
		}
		p.index++
		left = child
	default:
		return nil, fmt.Errorf("expected condition value at offset %d", current.pos)
	}
	for {
		operator := p.peek().text
		precedence := precedence(operator)
		if precedence < minimum {
			break
		}
		p.index++
		right, err := p.expression(precedence+1, depth+1)
		if err != nil {
			return nil, err
		}
		left = &node{op: operator, left: left, right: right, status: left.status || right.status, depth: max(left.depth, right.depth) + 1}
		if left.depth > maxDepth {
			return nil, fmt.Errorf("condition exceeds maximum depth %d", maxDepth)
		}
	}
	if left.depth > maxDepth {
		return nil, fmt.Errorf("condition exceeds maximum depth %d", maxDepth)
	}
	return left, nil
}

func precedence(operator string) int {
	switch operator {
	case "||":
		return 1
	case "&&":
		return 2
	case "==", "!=":
		return 3
	case "<", "<=", ">", ">=":
		return 4
	}
	return 0
}

func lex(source string) ([]token, error) {
	var tokens []token
	for index := 0; index < len(source); {
		if strings.ContainsRune(" \t\r\n", rune(source[index])) {
			index++
			continue
		}
		start := index
		if source[index] == '\'' {
			index++
			var text strings.Builder
			closed := false
			for index < len(source) {
				if source[index] == '\'' {
					index++
					if index < len(source) && source[index] == '\'' {
						text.WriteByte('\'')
						index++
						continue
					}
					closed = true
					break
				}
				text.WriteByte(source[index])
				index++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated string at offset %d", start)
			}
			tokens = append(tokens, token{kind: 'v', value: value{kind: 's', text: text.String()}, pos: start})
		} else if strings.ContainsRune("(),!<>=&|", rune(source[index])) {
			index++
			if index < len(source) {
				pair := source[start : index+1]
				if pair == "==" || pair == "!=" || pair == "<=" || pair == ">=" || pair == "&&" || pair == "||" {
					index++
				}
			}
			text := source[start:index]
			if text == "=" || text == "&" || text == "|" {
				return nil, fmt.Errorf("unsupported condition operator at offset %d", start)
			}
			tokens = append(tokens, token{kind: 'o', text: text, pos: start})
		} else {
			for index < len(source) && !strings.ContainsRune(" \t\r\n(),!<>=&|'", rune(source[index])) {
				index++
			}
			text := source[start:index]
			item := token{kind: 'v', pos: start}
			switch strings.ToLower(text) {
			case "true":
				item.value = boolean(true)
			case "false":
				item.value = boolean(false)
			case "null":
				item.value = value{}
			default:
				if text[0] >= '0' && text[0] <= '9' || text[0] == '-' || text[0] == '+' || text[0] == '.' {
					number := parseNumber(text)
					if math.IsNaN(number) {
						return nil, fmt.Errorf("invalid number at offset %d", start)
					}
					item.value = value{kind: 'n', number: number}
				} else if reference.MatchString(text) {
					item.kind, item.text = 'r', text
				} else {
					return nil, fmt.Errorf("unsupported condition syntax at offset %d", start)
				}
			}
			tokens = append(tokens, item)
		}
		if len(tokens) > maxTokens {
			return nil, fmt.Errorf("condition exceeds %d tokens", maxTokens)
		}
	}
	return append(tokens, token{kind: 'e', pos: len(source)}), nil
}
