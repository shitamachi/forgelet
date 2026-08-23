package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ContentClient reads repository files through the GitHub contents API
// (spec 0011 T7: workflow source replacement for the local directory).
type ContentClient struct {
	BaseURL string
	HTTP    *http.Client
	Tokens  TokenSource
}

// NewContentClient wires a ContentClient.
func NewContentClient(baseURL string, hc *http.Client, tokens TokenSource) *ContentClient {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	return &ContentClient{BaseURL: baseURL, HTTP: hc, Tokens: tokens}
}

// ContentFile is one fetched file.
type ContentFile struct {
	Name string
	Path string
	Data []byte
}

// DefaultBranch returns the repository's default branch name.
func (c *ContentClient) DefaultBranch(ctx context.Context, owner, name string) (string, error) {
	var repo struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := c.getJSON(ctx, fmt.Sprintf("%s/repos/%s/%s", c.BaseURL, owner, name), &repo); err != nil {
		return "", fmt.Errorf("github content: repo info: %w", err)
	}
	if repo.DefaultBranch == "" {
		return "", fmt.Errorf("github content: %s/%s has no default branch", owner, name)
	}
	return repo.DefaultBranch, nil
}

// WorkflowsDir lists and fetches the .yml/.yaml files under
// .github/workflows at the given ref.
func (c *ContentClient) WorkflowsDir(ctx context.Context, owner, name, ref string) ([]ContentFile, error) {
	var entries []struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Type string `json:"type"`
	}
	listURL := fmt.Sprintf("%s/repos/%s/%s/contents/.github/workflows?ref=%s", c.BaseURL, owner, name, ref)
	if err := c.getJSON(ctx, listURL, &entries); err != nil {
		return nil, fmt.Errorf("github content: list workflows: %w", err)
	}
	var out []ContentFile
	for _, e := range entries {
		if e.Type != "file" {
			continue
		}
		if !strings.HasSuffix(e.Name, ".yml") && !strings.HasSuffix(e.Name, ".yaml") {
			continue
		}
		var file struct {
			Content  string `json:"content"`
			Encoding string `json:"encoding"`
		}
		fileURL := fmt.Sprintf("%s/repos/%s/%s/contents/%s?ref=%s", c.BaseURL, owner, name, e.Path, ref)
		if err := c.getJSON(ctx, fileURL, &file); err != nil {
			return nil, fmt.Errorf("github content: fetch %s: %w", e.Path, err)
		}
		data, err := decodeContent(file.Encoding, file.Content)
		if err != nil {
			return nil, fmt.Errorf("github content: decode %s: %w", e.Path, err)
		}
		out = append(out, ContentFile{Name: e.Name, Path: e.Path, Data: data})
	}
	return out, nil
}

func decodeContent(encoding, content string) ([]byte, error) {
	if encoding != "base64" {
		return nil, fmt.Errorf("unsupported encoding %q", encoding)
	}
	return base64.StdEncoding.DecodeString(strings.Join(strings.Fields(content), ""))
}

func (c *ContentClient) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	token, err := c.Tokens.Token(ctx)
	if err != nil {
		return fmt.Errorf("token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}
