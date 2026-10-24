package github

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/shitamachi/forgelet/internal/run"
)

const maxWorkflowBytes = 256 << 10

type contentEntry struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Type     string `json:"type"`
	Size     int64  `json:"size"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
}

// Workflows reads a bounded set of YAML files from the exact event commit.
func (c *Client) Workflows(ctx context.Context, event run.Event) (map[string][]byte, error) {
	if !validRepository(event.Repository) || !validSHA(event.SHA) {
		return nil, errors.New("github: invalid workflow repository or commit SHA")
	}
	token, err := c.RepositoryToken(ctx, event.InstallationID, event.Repository)
	if err != nil {
		return nil, err
	}
	base := "/repos/" + event.Repository + "/contents/.github/workflows"
	query := url.Values{"ref": {event.SHA}}
	var entries []contentEntry
	if err := c.request(ctx, http.MethodGet, base, query, token, nil, &entries); err != nil {
		if isStatus(err, http.StatusNotFound) {
			return map[string][]byte{}, nil
		}
		return nil, fmt.Errorf("github: list workflows: %w", err)
	}
	if len(entries) >= 1000 {
		return nil, errors.New("github: workflow directory reaches API listing limit")
	}
	result := make(map[string][]byte)
	total := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name, ".yml") && !strings.HasSuffix(entry.Name, ".yaml") {
			continue
		}
		if entry.Type != "file" || path.Base(entry.Name) != entry.Name || strings.ContainsAny(entry.Name, "\\\x00\r\n") || entry.Path != ".github/workflows/"+entry.Name {
			return nil, errors.New("github: invalid workflow directory entry")
		}
		if len(result) >= 100 || entry.Size < 0 || entry.Size > maxWorkflowBytes {
			return nil, errors.New("github: workflow count or size exceeds limit")
		}
		if _, duplicate := result[entry.Path]; duplicate {
			return nil, errors.New("github: duplicate workflow entry")
		}
		var file contentEntry
		if err := c.request(ctx, http.MethodGet, base+"/"+entry.Name, query, token, nil, &file); err != nil {
			return nil, fmt.Errorf("github: read workflow: %w", err)
		}
		if file.Type != "file" || file.Path != entry.Path || file.Encoding != "base64" || file.Size < 0 || file.Size > maxWorkflowBytes {
			return nil, errors.New("github: invalid workflow file response")
		}
		decoded, err := base64.StdEncoding.DecodeString(file.Content)
		if err != nil || len(decoded) > maxWorkflowBytes {
			return nil, errors.New("github: invalid or oversized workflow content")
		}
		total += len(decoded)
		if total > 4<<20 {
			return nil, errors.New("github: total workflow content exceeds limit")
		}
		result[entry.Path] = decoded
	}
	return result, nil
}
