// Package httpclient implements the executor ControlPlane contract over
// the forgelet control-plane internal API (spec 0008 FR-X4). Requests carry
// the JobRun-scoped bearer token; secret values appear only in responses,
// never in errors or logs.
package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/shitamachi/forgelet/internal/run/plan"
	"github.com/shitamachi/forgelet/internal/runtime/executor"
	"github.com/shitamachi/forgelet/internal/security/identity"
)

// Client talks to the control plane internal API.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// New wires a Client.
func New(baseURL, token string, hc *http.Client) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{BaseURL: baseURL, Token: token, HTTP: hc}
}

// ClientError marks non-2xx API responses; retryable is true for 5xx and 429.
type ClientError struct {
	Op     string
	Status int
	Path   string
}

func (e *ClientError) Error() string {
	return fmt.Sprintf("control-plane %s %s: status %d", e.Op, e.Path, e.Status)
}

// Retryable reports whether the operation may be retried.
func (e *ClientError) Retryable() bool {
	return e.Status >= 500 || e.Status == http.StatusTooManyRequests
}

// FetchPlan implements executor.ControlPlane.
func (c *Client) FetchPlan(ctx context.Context, id identity.Identity) (plan.Plan, error) {
	var out plan.Plan
	path := fmt.Sprintf("/internal/jobruns/%s/plan", id.JobRunID)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return plan.Plan{}, err
	}
	return out, nil
}

// FetchSecrets implements executor.ControlPlane. The request body contains
// references only; the response maps "scope/name" to the resolved value.
func (c *Client) FetchSecrets(ctx context.Context, id identity.Identity, refs []plan.SecretRef) (map[string]string, error) {
	type refJSON struct {
		Scope string `json:"scope"`
		Name  string `json:"name"`
	}
	reqRefs := make([]refJSON, 0, len(refs))
	for _, r := range refs {
		reqRefs = append(reqRefs, refJSON{Scope: r.Scope, Name: r.Name})
	}
	var out map[string]string
	path := fmt.Sprintf("/internal/jobruns/%s/secrets", id.JobRunID)
	if err := c.do(ctx, http.MethodPost, path, reqRefs, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ReportJob implements executor.ControlPlane.
func (c *Client) ReportJob(ctx context.Context, id identity.Identity, result executor.JobResult) error {
	path := fmt.Sprintf("/internal/jobruns/%s/status", id.JobRunID)
	return c.do(ctx, http.MethodPost, path, result, nil)
}

// ResolveCache implements executor.ControlPlane.
func (c *Client) ResolveCache(ctx context.Context, id identity.Identity, key string, restoreKeys []string) (bool, string, string, error) {
	req := map[string]any{"key": key, "restoreKeys": restoreKeys}
	var resp struct {
		Hit    bool   `json:"hit"`
		GetURL string `json:"getUrl"`
		PutURL string `json:"putUrl"`
	}
	path := fmt.Sprintf("/internal/jobruns/%s/cache/resolve", id.JobRunID)
	if err := c.do(ctx, http.MethodPost, path, req, &resp); err != nil {
		return false, "", "", err
	}
	return resp.Hit, resp.GetURL, resp.PutURL, nil
}

// ArtifactUploadURL implements executor.ControlPlane.
func (c *Client) ArtifactUploadURL(ctx context.Context, id identity.Identity, name string) (string, error) {
	var resp struct {
		UploadURL string `json:"uploadUrl"`
	}
	path := fmt.Sprintf("/internal/jobruns/%s/artifacts/%s", id.JobRunID, name)
	if err := c.do(ctx, http.MethodPost, path, map[string]string{}, &resp); err != nil {
		return "", err
	}
	return resp.UploadURL, nil
}

// ArtifactDownloadURL implements executor.ControlPlane.
func (c *Client) ArtifactDownloadURL(ctx context.Context, id identity.Identity, name string) (string, error) {
	var resp struct {
		DownloadURL string `json:"downloadUrl"`
	}
	path := fmt.Sprintf("/internal/jobruns/%s/artifacts/%s", id.JobRunID, name)
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return "", err
	}
	return resp.DownloadURL, nil
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		buf, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("control-plane: encode request: %w", err)
		}
		body = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return fmt.Errorf("control-plane: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("control-plane: request: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Only method/path/status — never the body, which may echo secrets.
		return &ClientError{Op: method, Status: resp.StatusCode, Path: path}
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("control-plane: decode response: %w", err)
		}
	}
	return nil
}
