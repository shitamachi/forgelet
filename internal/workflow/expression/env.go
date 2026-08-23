package expression

import "strings"

// Env is an immutable registry of evaluation contexts. Names are matched
// case-insensitively (GitHub treats contexts as case-insensitive). Use
// different Envs for the scheduler-time and runtime phases; accessing an
// unregistered context yields a ContextUnavailableError, never null.
type Env struct {
	contexts map[string]Value
	hasher   WorkspaceHasher // capability behind hashFiles(); nil outside workspace phases
}

// NewEnv returns an empty environment.
func NewEnv() *Env {
	return &Env{contexts: map[string]Value{}}
}

// With returns a new Env with the named context registered (copy-on-write).
// The name is lowercased for lookup.
func (e *Env) With(name string, v Value) *Env {
	next := &Env{contexts: make(map[string]Value, len(e.contexts)+1), hasher: e.hasher}
	for k, val := range e.contexts {
		next.contexts[k] = val
	}
	next.contexts[strings.ToLower(name)] = v
	return next
}

// Lookup finds a context by (case-insensitive) name.
func (e *Env) Lookup(name string) (Value, bool) {
	v, ok := e.contexts[strings.ToLower(name)]
	return v, ok
}

// Available lists registered context names in lowercase.
func (e *Env) Available() []string {
	names := make([]string, 0, len(e.contexts))
	for k := range e.contexts {
		names = append(names, k)
	}
	return names
}
