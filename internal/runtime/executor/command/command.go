// Package command parses GitHub Actions workflow commands emitted to stdout
// or stderr: `::name param=value,param2=value2::message`. Pure functions.
package command

import (
	"strings"
)

// Known workflow command names.
const (
	AddMask  = "add-mask"
	Group    = "group"
	EndGroup = "endgroup"
	Warning  = "warning"
	Error    = "error"
	Notice   = "notice"
	Debug    = "debug"
)

// Command is a parsed workflow command.
type Command struct {
	Name       string
	Parameters map[string]string
	Message    string
}

// Parse recognizes a workflow command line. ok=false for ordinary output.
func Parse(line string) (Command, bool) {
	if !strings.HasPrefix(line, "::") {
		return Command{}, false
	}
	rest := line[2:]
	end := strings.Index(rest, "::")
	if end < 0 {
		return Command{}, false
	}
	head, message := rest[:end], rest[end+2:]

	c := Command{Message: message}
	name := head
	if idx := strings.Index(head, " "); idx >= 0 {
		name = head[:idx]
		c.Parameters = parseParams(head[idx+1:])
	}
	c.Name = strings.ToLower(strings.TrimSpace(name))
	switch c.Name {
	case AddMask, Group, EndGroup, Warning, Error, Notice, Debug:
		return c, true
	default:
		return Command{}, false
	}
}

func parseParams(s string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		if part == "" {
			continue
		}
		if eq := strings.Index(part, "="); eq > 0 {
			out[part[:eq]] = part[eq+1:]
		}
	}
	return out
}
