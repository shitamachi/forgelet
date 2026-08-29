package github

import (
	"context"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ActionMeta is the parsed action.yml metadata needed to decide how to run
// the action. Only the fields used by forgelet are modelled.
type ActionMeta struct {
	RunsUsing string
	Main      string
	Inputs    map[string]struct{}
	Steps     []CompositeActionStep // only for runs.using: composite
}

// CompositeActionStep is one step inside a composite action's runs.steps.
type CompositeActionStep struct {
	Name string            `yaml:"name"`
	Run  string            `yaml:"run"`
	Uses string            `yaml:"uses"`
	With map[string]string `yaml:"with"`
}

// ActionFetcher fetches action metadata via the content API.
type ActionFetcher interface {
	FetchAction(ctx context.Context, owner, repo, ref, path string) (*ActionMeta, error)
}

// FetchAction implements ActionFetcher on ContentClient. path is the
// subdirectory inside the action repo (empty for root). It tries
// action.yml then action.yaml.
func (c *ContentClient) FetchAction(ctx context.Context, owner, repo, ref, subpath string) (*ActionMeta, error) {
	candidates := []string{"action.yml", "action.yaml"}
	if subpath != "" {
		for i, c := range candidates {
			candidates[i] = strings.Trim(subpath, "/") + "/" + c
		}
	}
	var lastErr error
	for _, p := range candidates {
		data, err := c.fetchFile(ctx, owner, repo, p, ref)
		if err != nil {
			lastErr = err
			continue
		}
		meta, err := parseActionMeta(data)
		if err != nil {
			return nil, err
		}
		return meta, nil
	}
	if lastErr != nil {
		return nil, fmt.Errorf("github action: %s/%s@%s: %w", owner, repo, ref, lastErr)
	}
	return nil, fmt.Errorf("github action: %s/%s@%s: action.yml not found", owner, repo, ref)
}

func (c *ContentClient) fetchFile(ctx context.Context, owner, repo, path, ref string) ([]byte, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/contents/%s?ref=%s", c.BaseURL, owner, repo, path, ref)
	var file struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := c.getJSON(ctx, url, &file); err != nil {
		return nil, err
	}
	return decodeContent(file.Encoding, file.Content)
}

func parseActionMeta(data []byte) (*ActionMeta, error) {
	var raw struct {
		Inputs map[string]struct {
			Description string `yaml:"description"`
			Required    bool   `yaml:"required"`
		} `yaml:"inputs"`
		Runs struct {
			Using string `yaml:"using"`
			Main  string `yaml:"main"`
			Steps []CompositeActionStep `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse action.yml: %w", err)
	}
	meta := &ActionMeta{
		RunsUsing: strings.ToLower(raw.Runs.Using),
		Main:      raw.Runs.Main,
		Inputs:    map[string]struct{}{},
		Steps:     raw.Runs.Steps,
	}
	for k := range raw.Inputs {
		meta.Inputs[k] = struct{}{}
	}
	return meta, nil
}

// ParseUses splits "owner/repo[/path]@ref" into components.
func ParseUses(uses string) (owner, repo, path, ref string, err error) {
	uses = strings.TrimSpace(uses)
	at := strings.LastIndex(uses, "@")
	if at < 0 {
		return "", "", "", "", fmt.Errorf("uses %q must be <owner>/<repo>[@<ref>]", uses)
	}
	ref = uses[at+1:]
	repoPart := uses[:at]
	if ref == "" {
		return "", "", "", "", fmt.Errorf("uses %q must be <owner>/<repo>@<ref>", uses)
	}
	parts := strings.Split(repoPart, "/")
	if len(parts) < 2 {
		return "", "", "", "", fmt.Errorf("uses %q must be <owner>/<repo>[@<ref>]", uses)
	}
	owner = parts[0]
	repo = parts[1]
	if len(parts) > 2 {
		path = strings.Join(parts[2:], "/")
	}
	if owner == "" || repo == "" {
		return "", "", "", "", fmt.Errorf("uses %q must be <owner>/<repo>[@<ref>]", uses)
	}
	return owner, repo, path, ref, nil
}
