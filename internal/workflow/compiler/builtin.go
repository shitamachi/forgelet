package compiler

import (
	"fmt"
	"strings"

	"github.com/shitamachi/forgelet/internal/workflow/syntax"
)

// Builtin describes one whitelisted builtin action and its supported
// `with:` input keys (spec 0009 FR-A1).
type Builtin struct {
	Name   string
	Inputs []string
}

// Registry is the fixed builtin whitelist. Anything else fails compilation;
// there is deliberately no network download path (FR-A1.2).
var Registry = map[string]Builtin{
	"actions/checkout": {
		Name:   "actions/checkout",
		Inputs: []string{"repository", "ref", "token", "fetch-depth", "persist-credentials"},
	},
	"actions/cache": {
		Name:   "actions/cache",
		Inputs: []string{"key", "restore-keys", "path"},
	},
	"actions/upload-artifact": {
		Name:   "actions/upload-artifact",
		Inputs: []string{"name", "path", "retention-days"},
	},
	"actions/download-artifact": {
		Name:   "actions/download-artifact",
		Inputs: []string{"name", "path"},
	},
}

// BuiltinCall is a compiled `uses:` reference: the canonical registry name,
// the pinned ref and its inputs (FR-A1.1).
type BuiltinCall struct {
	Action  string
	Version string
	Inputs  map[string]string
}

// Warning is a non-blocking diagnostic attached to the compiled workflow
// (FR-A1.3: unknown `with:` keys warn instead of failing).
type Warning struct {
	Job  string
	Step int
	Msg  string
}

// parseUses splits `owner/repo@ref` into registry key and version.
func parseUses(uses string) (action, version string, err error) {
	action, version, ok := strings.Cut(strings.TrimSpace(uses), "@")
	if !ok || action == "" || version == "" {
		return "", "", fmt.Errorf("uses %q must be <owner>/<repo>@<ref>", uses)
	}
	if _, ok := Registry[action]; !ok {
		hint := nearest(action)
		if hint != "" {
			return "", "", fmt.Errorf("action %q is not a forgelet builtin; did you mean %q?", uses, hint)
		}
		return "", "", fmt.Errorf("action %q is not a forgelet builtin (available: %s)",
			uses, strings.Join(registryNames(), ", "))
	}
	return action, version, nil
}

func registryNames() []string {
	names := make([]string, 0, len(Registry))
	for n := range Registry {
		names = append(names, n)
	}
	sortStrings(names)
	return names
}

// nearest returns the registry entry within edit distance 2 of s, if any.
func nearest(s string) string {
	best, bestDist := "", 3
	for n := range Registry {
		if d := editDistance(strings.ToLower(s), n); d < bestDist {
			best, bestDist = n, d
		}
	}
	return best
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = minInt(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func minInt(a, b int, rest ...int) int {
	m := a
	if b < m {
		m = b
	}
	for _, r := range rest {
		if r < m {
			m = r
		}
	}
	return m
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// compileBuiltin resolves one uses step against the registry and reports
// unsupported `with:` keys as warnings.
func compileBuiltin(jobID string, idx int, st *syntax.Step) (*BuiltinCall, Warning, error) {
	action, version, err := parseUses(st.Uses)
	if err != nil {
		return nil, Warning{}, err
	}
	spec := Registry[action]
	supported := make(map[string]bool, len(spec.Inputs))
	for _, k := range spec.Inputs {
		supported[k] = true
	}
	var warn Warning
	inputs := make(map[string]string, len(st.With))
	for k, v := range st.With {
		if !supported[k] {
			warn = Warning{Job: jobID, Step: idx,
				Msg: fmt.Sprintf("input %q is not supported by %s", k, action)}
			continue
		}
		inputs[k] = v
	}
	return &BuiltinCall{Action: action, Version: version, Inputs: inputs}, warn, nil
}
