package expression

import (
	"fmt"
)

// node is the internal AST; not exported (spec FR-2.1 discipline: syntax
// never leaves the module).
type node interface{}

type literalNode struct{ v Value }

type identNode struct{ name string }

type memberNode struct {
	obj  node
	prop node
}

type unaryNode struct {
	op string
	x  node
}

type binaryNode struct {
	op   string
	l, r node
}

type callNode struct {
	name string
	args []node
}

type parser struct {
	expr string
	toks []token
	pos  int
}

func parse(expr string) (node, error) {
	lex := newLexer(expr)
	toks, err := lex.tokens()
	if err != nil {
		return nil, err
	}
	p := &parser{expr: expr, toks: toks}
	n, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.cur().kind != tkEOF {
		return nil, p.errAt(p.cur(), "unexpected %s after expression", describe(p.cur()))
	}
	return n, nil
}

func (p *parser) cur() token { return p.toks[p.pos] }

func (p *parser) next() token {
	t := p.toks[p.pos]
	if t.kind != tkEOF {
		p.pos++
	}
	return t
}

func (p *parser) errAt(tok token, format string, args ...any) *ParseError {
	return &ParseError{Expr: p.expr, Offset: tok.start, Line: tok.line, Column: tok.col,
		Msg: fmt.Sprintf(format, args...)}
}

func describe(t token) string {
	switch t.kind {
	case tkEOF:
		return "end of expression"
	case tkIdent:
		return fmt.Sprintf("identifier %q", t.text)
	case tkNumber:
		return fmt.Sprintf("number %s", t.text)
	case tkString:
		return fmt.Sprintf("string %q", t.str)
	default:
		return fmt.Sprintf("%q", t.text)
	}
}

// parseOr: a || a || ...
func (p *parser) parseOr() (node, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.match("||") {
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &binaryNode{op: "||", l: left, r: right}
	}
	return left, nil
}

func (p *parser) parseAnd() (node, error) {
	left, err := p.parseCompare()
	if err != nil {
		return nil, err
	}
	for p.match("&&") {
		right, err := p.parseCompare()
		if err != nil {
			return nil, err
		}
		left = &binaryNode{op: "&&", l: left, r: right}
	}
	return left, nil
}

func (p *parser) parseCompare() (node, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		t := p.cur()
		if t.kind != tkPunct {
			return left, nil
		}
		switch t.text {
		case "==", "!=", "<", "<=", ">", ">=":
			p.next()
			right, err := p.parseUnary()
			if err != nil {
				return nil, err
			}
			left = &binaryNode{op: t.text, l: left, r: right}
		default:
			return left, nil
		}
	}
}

func (p *parser) parseUnary() (node, error) {
	if p.match("!") {
		x, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &unaryNode{op: "!", x: x}, nil
	}
	return p.parsePostfix()
}

func (p *parser) parsePostfix() (node, error) {
	x, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	for {
		switch {
		case p.match("."):
			prop, err := p.parsePrimary()
			if err != nil {
				return nil, err
			}
			// a.b — the property must be an identifier or a number (a.0).
			switch pn := prop.(type) {
			case *identNode:
				x = &memberNode{obj: x, prop: &literalNode{v: StrOf(pn.name)}}
			case *literalNode:
				if pn.v.Kind == KindNumber {
					x = &memberNode{obj: x, prop: pn}
				} else {
					return nil, p.errAt(p.cur(), "expected property name after '.'")
				}
			default:
				return nil, p.errAt(p.cur(), "expected property name after '.'")
			}
		case p.match("["):
			idx, err := p.parseOr()
			if err != nil {
				return nil, err
			}
			if !p.match("]") {
				return nil, p.errAt(p.cur(), "expected ']'")
			}
			x = &memberNode{obj: x, prop: idx}
		default:
			return x, nil
		}
	}
}

func (p *parser) parsePrimary() (node, error) {
	t := p.cur()
	switch t.kind {
	case tkNumber:
		p.next()
		return &literalNode{v: NumOf(t.num)}, nil
	case tkString:
		p.next()
		return &literalNode{v: StrOf(t.str)}, nil
	case tkIdent:
		p.next()
		switch t.text {
		case "null":
			return &literalNode{v: Null}, nil
		case "true":
			return &literalNode{v: BoolOf(true)}, nil
		case "false":
			return &literalNode{v: BoolOf(false)}, nil
		}
		// Function call: parse the shape now, reject at evaluation so V1
		// can register functions without a parser change.
		if p.match("(") {
			call := &callNode{name: t.text}
			if !p.match(")") {
				for {
					arg, err := p.parseOr()
					if err != nil {
						return nil, err
					}
					call.args = append(call.args, arg)
					if p.match(",") {
						continue
					}
					if p.match(")") {
						break
					}
					return nil, p.errAt(p.cur(), "expected ',' or ')' in argument list")
				}
			}
			return call, nil
		}
		return &identNode{name: t.text}, nil
	case tkPunct:
		if t.text == "(" {
			p.next()
			inner, err := p.parseOr()
			if err != nil {
				return nil, err
			}
			if !p.match(")") {
				return nil, p.errAt(p.cur(), "expected ')'")
			}
			return inner, nil
		}
	}
	return nil, p.errAt(t, "unexpected %s", describe(t))
}

func (p *parser) match(text string) bool {
	t := p.cur()
	if t.kind == tkPunct && t.text == text {
		p.next()
		return true
	}
	return false
}
