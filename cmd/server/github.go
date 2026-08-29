package main

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"strings"

	"github.com/shitamachi/forgelet/internal/provider/github"
	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/server"
	"github.com/shitamachi/forgelet/internal/workflow/syntax"
)

// staticToken is a fixed bearer credential (PAT or pre-minted token).
type staticToken string

// Token implements github.TokenSource.
func (s staticToken) Token(context.Context) (string, error) { return string(s), nil }

// githubSource adapts the provider content client to the server's
// repository workflow-source port (fetcher + default-branch resolver).
type githubSource struct{ c *github.ContentClient }

// FetchWorkflows implements server.WorkflowFetcher at the given ref.
func (g githubSource) FetchWorkflows(ctx context.Context, repo model.RepositoryRef, ref string) ([]server.WorkflowFile, error) {
	files, err := g.c.WorkflowsDir(ctx, repo.Owner, repo.Name, syntax.TrimRefPrefix(ref))
	if err != nil {
		return nil, err
	}
	out := make([]server.WorkflowFile, 0, len(files))
	for _, f := range files {
		out = append(out, server.WorkflowFile{Name: f.Name, Data: f.Data})
	}
	return out, nil
}

// DefaultBranch implements server.DefaultBrancher.
func (g githubSource) DefaultBranch(ctx context.Context, repo model.RepositoryRef) (string, error) {
	return g.c.DefaultBranch(ctx, repo.Owner, repo.Name)
}

// FetchAction implements github.ActionFetcher.
func (g githubSource) FetchAction(ctx context.Context, owner, repo, ref, subpath string) (*github.ActionMeta, error) {
	return g.c.FetchAction(ctx, owner, repo, ref, subpath)
}

// loadAppKey reads a GitHub App private key (PKCS#1 or PKCS#8 PEM).
func loadAppKey(path string) (*rsa.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read app key: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("app key %s: no PEM block", path)
	}
	if k, kerr := x509.ParsePKCS1PrivateKey(block.Bytes); kerr == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse app key: %w", err)
	}
	rsaKey, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("app key %s: not an RSA private key", path)
	}
	return rsaKey, nil
}

// parseRepos parses a comma-separated owner/name list.
func parseRepos(flagValue string) ([]server.ScheduledRepo, error) {
	if flagValue == "" {
		return nil, nil
	}
	var out []server.ScheduledRepo
	for _, part := range strings.Split(flagValue, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		owner, name, ok := strings.Cut(part, "/")
		if !ok || owner == "" || name == "" {
			return nil, fmt.Errorf("invalid -scheduled-repos entry %q (want owner/name)", part)
		}
		out = append(out, server.ScheduledRepo{Owner: owner, Name: name})
	}
	return out, nil
}
