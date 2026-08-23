// Package filecommand parses the GitHub Actions file commands written by
// steps: GITHUB_ENV / GITHUB_OUTPUT (key=value and heredoc) and GITHUB_PATH
// (one path per line). Pure functions, stdlib only.
package filecommand

import (
	"fmt"
	"strings"
)

// KV is one parsed key/value pair in file order.
type KV struct {
	Key   string
	Value string
}

// ParseKVFile parses GITHUB_ENV / GITHUB_OUTPUT content. Duplicate keys keep
// the last occurrence (GitHub semantics: later lines win).
func ParseKVFile(data []byte) ([]KV, error) {
	var out []KV
	lines := splitLines(string(data))
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			continue
		}
		if idx := strings.Index(line, "<<"); idx > 0 {
			key := strings.TrimSpace(line[:idx])
			delim := strings.TrimSpace(line[idx+2:])
			if key == "" || delim == "" {
				return nil, fmt.Errorf("filecommand: malformed heredoc opener %q", line)
			}
			var body []string
			closed := false
			for i++; i < len(lines); i++ {
				if lines[i] == delim {
					closed = true
					break
				}
				body = append(body, lines[i])
			}
			if !closed {
				return nil, fmt.Errorf("filecommand: heredoc for %q not closed by %q", key, delim)
			}
			out = append(out, KV{Key: key, Value: strings.Join(body, "\n")})
			continue
		}
		eq := strings.Index(line, "=")
		if eq <= 0 {
			return nil, fmt.Errorf("filecommand: malformed line %q (want NAME=value or NAME<<DELIM)", line)
		}
		out = append(out, KV{Key: strings.TrimSpace(line[:eq]), Value: line[eq+1:]})
	}
	return out, nil
}

// Apply folds parsed KVs into env; later occurrences overwrite earlier ones.
func Apply(env map[string]string, kvs []KV) map[string]string {
	for _, kv := range kvs {
		env[kv.Key] = kv.Value
	}
	return env
}

// ParsePathFile parses GITHUB_PATH content: one filesystem path per line,
// blank lines ignored.
func ParsePathFile(data []byte) []string {
	var out []string
	for _, line := range splitLines(string(data)) {
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}
