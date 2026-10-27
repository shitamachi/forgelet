package expression

import (
	"fmt"
	"strings"

	"github.com/rhysd/actionlint"
)

// actionlint 1.7.12 parses mixed comparisons right-associatively. Explicit
// grouping preserves GitHub's operator precedence before handing off its AST.
func groupComparisons(source string) (string, error) {
	lexer := actionlint.NewExprLexer(source + "}}")
	var tokens []*actionlint.Token
	depth := 0
	for {
		token := lexer.Next()
		if token.Kind == actionlint.TokenKindEnd {
			if lexer.Offset() != len(source)+2 {
				return "", fmt.Errorf("trailing expression content")
			}
			break
		}
		if token.Kind == actionlint.TokenKindUnknown {
			return "", fmt.Errorf("invalid expression: %w", lexer.Err())
		}
		if len(tokens) >= maxTokens {
			return "", fmt.Errorf("expression exceeds token limit")
		}
		switch token.Value {
		case "(", "[":
			depth++
		case ")", "]":
			depth--
		}
		if depth > maxDepth {
			return "", fmt.Errorf("expression exceeds depth limit")
		}
		tokens = append(tokens, token)
	}
	if len(tokens) == 0 {
		return source, nil
	}
	var group func(int, int) string
	group = func(lo, hi int) string {
		if lo >= hi {
			return ""
		}
		level, best, split := 0, 100, -1
		for i := lo; i < hi; i++ {
			v := tokens[i].Value
			if v == "(" || v == "[" {
				level++
				continue
			}
			if v == ")" || v == "]" {
				level--
				continue
			}
			if level != 0 {
				continue
			}
			rank := 100
			switch v {
			case ",":
				rank = 0
			case "||":
				rank = 1
			case "&&":
				rank = 2
			case "==", "!=":
				rank = 3
			case "<", ">", "<=", ">=":
				rank = 4
			}
			if rank < 100 && rank <= best {
				best, split = rank, i
			}
		}
		if split >= 0 {
			v := tokens[split].Value
			joined := group(lo, split) + " " + v + " " + group(split+1, hi)
			if v == "," {
				return joined
			}
			return "(" + joined + ")"
		}
		var out strings.Builder
		for i := lo; i < hi; i++ {
			v := tokens[i].Value
			start := tokens[i].Offset
			end := len(source)
			if i+1 < len(tokens) {
				end = tokens[i+1].Offset
			}
			if v == "(" || v == "[" {
				closing := ")"
				if v == "[" {
					closing = "]"
				}
				nest, j := 1, i+1
				for ; j < hi; j++ {
					if tokens[j].Value == v {
						nest++
					}
					if tokens[j].Value == closing {
						nest--
						if nest == 0 {
							break
						}
					}
				}
				if j < hi {
					out.WriteString(v)
					out.WriteString(group(i+1, j))
					out.WriteString(closing)
					i = j
					continue
				}
			}
			out.WriteString(source[start:end])
		}
		return out.String()
	}
	return strings.TrimSpace(group(0, len(tokens))), nil
}
