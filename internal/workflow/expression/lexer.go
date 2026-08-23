package expression

import (
	"fmt"
	"strconv"
	"strings"
)

type tokenKind uint8

const (
	tkEOF tokenKind = iota
	tkIdent
	tkNumber
	tkString
	tkPunct // ! && || == != < <= > >= . [ ] ( ) , *
)

type token struct {
	kind  tokenKind
	text  string // raw text (idents, punctuation); decoded value for strings/numbers kept in num/str)
	num   float64
	str   string
	start int // byte offset
	line  int
	col   int
}

type lexer struct {
	src  string
	pos  int
	line int
	col  int
}

func newLexer(src string) *lexer {
	return &lexer{src: src, line: 1, col: 1}
}

func (l *lexer) errf(tok token, format string, args ...any) *ParseError {
	return &ParseError{Expr: l.src, Offset: tok.start, Line: tok.line, Column: tok.col,
		Msg: fmt.Sprintf(format, args...)}
}

func (l *lexer) peek() byte {
	if l.pos >= len(l.src) {
		return 0
	}
	return l.src[l.pos]
}

func (l *lexer) advance() byte {
	c := l.src[l.pos]
	l.pos++
	if c == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	return c
}

// tokens tokenizes the whole input.
func (l *lexer) tokens() ([]token, error) {
	var out []token
	for {
		tok, err := l.next()
		if err != nil {
			return nil, err
		}
		out = append(out, tok)
		if tok.kind == tkEOF {
			return out, nil
		}
	}
}

func (l *lexer) here() token {
	return token{start: l.pos, line: l.line, col: l.col}
}

func (l *lexer) next() (token, error) {
	// Skip whitespace and commas? Commas only appear inside call arguments;
	// treat them as punct and let the parser reject them outside calls.
	for l.pos < len(l.src) {
		c := l.peek()
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			l.advance()
			continue
		}
		break
	}
	start := l.here()
	if l.pos >= len(l.src) {
		return token{kind: tkEOF, start: start.start, line: start.line, col: start.col}, nil
	}

	c := l.peek()
	switch {
	case isIdentStart(c):
		for l.pos < len(l.src) && isIdentPart(l.peek()) {
			start.text += string(l.advance())
		}
		start.kind = tkIdent
		return start, nil
	case c >= '0' && c <= '9' || (c == '-' && l.pos+1 < len(l.src) && l.src[l.pos+1] >= '0' && l.src[l.pos+1] <= '9'):
		// A leading '-' is only valid as a negative literal (e.g. index
		// [-1]); binary subtraction is rejected separately below.
		digits := &strings.Builder{}
		hasDot := false
		if c == '-' {
			digits.WriteByte(l.advance())
		}
		for l.pos < len(l.src) {
			ch := l.peek()
			if ch >= '0' && ch <= '9' {
				digits.WriteByte(l.advance())
				continue
			}
			if ch == '.' && !hasDot && l.pos+1 < len(l.src) && l.src[l.pos+1] >= '0' && l.src[l.pos+1] <= '9' {
				hasDot = true
				digits.WriteByte(l.advance())
				continue
			}
			break
		}
		num, err := strconv.ParseFloat(digits.String(), 64)
		if err != nil {
			return token{}, l.errf(start, "bad number %q", digits.String())
		}
		start.kind, start.num, start.text = tkNumber, num, digits.String()
		return start, nil
	case c == '\'':
		l.advance() // opening quote
		var sb strings.Builder
		for {
			if l.pos >= len(l.src) {
				return token{}, l.errf(start, "unterminated string")
			}
			ch := l.advance()
			if ch == '\'' {
				// '' escapes a single quote inside the string.
				if l.pos < len(l.src) && l.peek() == '\'' {
					sb.WriteByte(l.advance())
					continue
				}
				break
			}
			sb.WriteByte(ch)
		}
		start.kind, start.str = tkString, sb.String()
		return start, nil
	}

	// Punctuation (longest match first).
	for _, op := range []string{"&&", "||", "==", "!=", "<=", ">="} {
		if strings.HasPrefix(l.src[l.pos:], op) {
			for range op {
				l.advance()
			}
			return token{kind: tkPunct, text: op, start: start.start, line: start.line, col: start.col}, nil
		}
	}
	switch c {
	case '!', '<', '>', '.', '[', ']', '(', ')', ',':
		l.advance()
		return token{kind: tkPunct, text: string(c), start: start.start, line: start.line, col: start.col}, nil
	case '"':
		return token{}, l.errf(start, "double-quoted strings are not supported; use single quotes")
	case '+', '-', '*', '/', '%':
		return token{}, l.errf(start, "arithmetic operator %q is not in the supported subset", string(c))

	default:
		return token{}, l.errf(start, "unexpected character %q", string(c))
	}
}

func isIdentStart(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || c >= '0' && c <= '9' || c == '-'
}
