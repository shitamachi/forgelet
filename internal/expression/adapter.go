package expression

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/rhysd/actionlint"

	"github.com/shitamachi/forgelet/internal/expression/acteval"
)

func normalized(source string) (string, error) {
	if len(source) > maxBytes {
		return "", fmt.Errorf("expression exceeds %d bytes", maxBytes)
	}
	source = strings.TrimSpace(source)
	if strings.HasPrefix(source, "${{") {
		if !strings.HasSuffix(source, "}}") {
			return "", errors.New("unterminated expression")
		}
		source = strings.TrimSpace(source[3 : len(source)-2])
		if source == "" {
			return "", errors.New("empty expression")
		}
	}
	return groupComparisons(source)
}
func referencePath(n actionlint.ExprNode, source string) string {
	path, _ := referenceSpan(n, source)
	return path
}
func referenceSpan(n actionlint.ExprNode, source string) (string, int) {
	switch n := n.(type) {
	case *actionlint.VariableNode:
		token := n.Token()
		return strings.ToLower(token.Value), token.Offset + len(token.Value)
	case *actionlint.ObjectDerefNode:
		path, end := referenceSpan(n.Receiver, source)
		if path == "" {
			return "", 0
		}
		lexer := actionlint.NewExprLexer(source[end:] + "}}")
		dot := lexer.Next()
		property := lexer.Next()
		if dot.Value != "." {
			return "", 0
		}
		return path + "." + property.Value, end + property.Offset + len(property.Value)
	case *actionlint.ArrayDerefNode:
		path, end := referenceSpan(n.Receiver, source)
		if path == "" {
			return "", 0
		}
		lexer := actionlint.NewExprLexer(source[end:] + "}}")
		lexer.Next()
		star := lexer.Next()
		return path + ".*", end + star.Offset + len(star.Value)
	case *actionlint.IndexAccessNode:
		path, end := referenceSpan(n.Operand, source)
		if path == "" {
			return "", 0
		}
		lexer := actionlint.NewExprLexer(source[end:] + "}}")
		depth := 0
		finish := end
		for {
			tok := lexer.Next()
			if tok.Kind == actionlint.TokenKindEnd || tok.Kind == actionlint.TokenKindUnknown {
				break
			}
			if tok.Value == "[" {
				depth++
			}
			if tok.Value == "]" {
				depth--
				if depth == 0 {
					finish = end + tok.Offset + 1
					break
				}
			}
		}
		key := "*"
		if text, ok := n.Index.(*actionlint.StringNode); ok {
			key = text.Value
		}
		return path + "." + key, finish
	}
	return "", 0
}

func receiver(parent, child actionlint.ExprNode) bool {
	switch p := parent.(type) {
	case *actionlint.ObjectDerefNode:
		return p.Receiver == child
	case *actionlint.ArrayDerefNode:
		return p.Receiver == child
	case *actionlint.IndexAccessNode:
		return p.Operand == child
	}
	return false
}
func syntax(source string, allowed func(string) bool) (actionlint.ExprNode, bool, error) {
	if source == "" {
		return nil, false, nil
	}
	lex := actionlint.NewExprLexer(source + "}}")
	for count := 0; ; count++ {
		token := lex.Next()
		if count > maxTokens {
			return nil, false, errors.New("expression exceeds token limit")
		}
		if token.Kind == actionlint.TokenKindEnd {
			if lex.Offset() != len(source)+2 {
				return nil, false, errors.New("trailing expression content")
			}
			break
		}
		if token.Kind == actionlint.TokenKindUnknown {
			return nil, false, fmt.Errorf("invalid expression: %w", lex.Err())
		}
	}
	node, err := actionlint.NewExprParser().Parse(actionlint.NewExprLexer(source + "}}"))
	if err != nil {
		return nil, false, fmt.Errorf("parse expression: %s", err.Message)
	}
	depth, status := 0, false
	var problem error
	actionlint.VisitExprNode(node, func(n, parent actionlint.ExprNode, enter bool) {
		if !enter {
			depth--
			return
		}
		depth++
		if depth > maxDepth {
			problem = errors.New("expression exceeds depth limit")
			return
		}
		if f, ok := n.(*actionlint.FuncCallNode); ok {
			name := strings.ToLower(f.Callee)
			arity, ok := functionArity[name]
			if !ok || len(f.Args) < arity[0] || len(f.Args) > arity[1] {
				problem = fmt.Errorf("unsupported function or arity %s", name)
			}
			status = status || name == "success" || name == "failure" || name == "cancelled" || name == "always"
		}
		if ref := referencePath(n, source); ref != "" && !receiver(parent, n) {
			root := strings.SplitN(ref, ".", 2)[0]
			known := false
			for _, key := range []string{"github", "inputs", "env", "vars", "secrets", "matrix", "needs", "jobs", "steps", "runner", "strategy", "job"} {
				known = known || root == key
			}
			if !known || allowed != nil && !allowed(ref) {
				problem = fmt.Errorf("unsupported context reference %q", ref)
			}
		}
	})
	return node, status, problem
}
func Validate(source string, allowed func(string) bool) error {
	source, err := normalized(source)
	if err != nil {
		return err
	}
	_, _, err = syntax(source, allowed)
	return err
}
func Evaluate(source string, ctx Context) (bool, error) {
	source, err := normalized(source)
	if err != nil {
		return false, err
	}
	_, status, err := syntax(source, nil)
	if err != nil {
		return false, err
	}
	if !status && !ctx.Success {
		return false, nil
	}
	if source == "" {
		return ctx.Success, nil
	}
	v, err := evaluate(source, ctx)
	return acteval.IsTruthy(v), err
}
func Eval(source string, ctx Context) (any, error) {
	source, err := normalized(source)
	if err != nil {
		return nil, err
	}
	if _, _, err = syntax(source, nil); err != nil {
		return nil, err
	}
	if source == "" {
		return nil, nil
	}
	return evaluate(source, ctx)
}
func setContext(root map[string]any, key string, value any) {
	parts := strings.Split(key, ".")
	for _, p := range parts[:len(parts)-1] {
		next, ok := root[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			root[p] = next
		}
		root = next
	}
	root[parts[len(parts)-1]] = value
}
func evaluate(source string, ctx Context) (result any, err error) {
	// Contain upstream reflection errors without exposing data (which may include secrets).
	defer func() {
		if recover() != nil {
			result = nil
			err = errors.New("expression value rejected by interpreter")
		}
	}()
	data := map[string]any{}
	keys := []string{}
	for key := range ctx.Typed {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) < len(keys[j]) })
	for _, key := range keys {
		raw, e := json.Marshal(ctx.Typed[key])
		if e != nil {
			return nil, e
		}
		var v any
		if e = json.Unmarshal(raw, &v); e != nil {
			return nil, e
		}
		setContext(data, key, v)
	}
	for key, value := range ctx.Values {
		if _, typed := ctx.Typed[key]; !typed {
			setContext(data, key, value)
		}
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	var env acteval.EvaluationEnvironment
	if err = json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("build expression context: %w", err)
	}
	if env.Github == nil {
		env.Github = map[string]interface{}{}
	}
	if env.Jobs == nil {
		env.Jobs = map[string]interface{}{}
	}
	if env.Job == nil {
		env.Job = map[string]interface{}{}
	}
	env.HashFiles = func([]reflect.Value) (interface{}, error) {
		return nil, errors.New("hashFiles requires an executor workspace capability")
	}
	node, _, _ := syntax(source, nil)
	type edit struct {
		start, end int
		value      string
	}
	var edits []edit
	actionlint.VisitExprNode(node, func(n, _ actionlint.ExprNode, enter bool) {
		if !enter {
			return
		}
		f, ok := n.(*actionlint.FuncCallNode)
		if !ok {
			return
		}
		var v bool
		switch strings.ToLower(f.Callee) {
		case "success":
			v = ctx.Success
		case "failure":
			v = ctx.Failure
		case "cancelled":
			v = ctx.Cancelled
		case "always":
			v = true
		default:
			return
		}
		start := f.Token().Offset
		end := strings.IndexByte(source[start:], ')') + start + 1
		edits = append(edits, edit{start, end, fmt.Sprint(v)})
	})
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	for _, e := range edits {
		source = source[:e.start] + e.value + source[e.end:]
	}
	result, err = acteval.NewInterpreter(&env).Evaluate(source)
	if err != nil {
		return nil, err
	}
	if raw, err = json.Marshal(result); err != nil || len(raw) > 64<<10 {
		return nil, errors.New("expression result exceeds 64 KiB or is not serializable")
	}
	return result, nil
}
