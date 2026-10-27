// Package expression adapts the actionlint AST and the bounded act interpreter.
package expression

type Context struct {
	Values                      map[string]string
	Typed                       map[string]any
	Success, Failure, Cancelled bool
}

const (
	maxBytes  = 8192
	maxTokens = 1024
	maxDepth  = 64
)
