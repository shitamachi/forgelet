package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/shitamachi/forgelet/internal/report"
	"github.com/shitamachi/forgelet/internal/run/model"
)

// CheckReporter reports job state as GitHub Check Runs. It upserts by the
// forgelet external ID: one job run maps to exactly one check run across
// retries (FR-G3.2).
type CheckReporter struct {
	BaseURL string
	HTTP    *http.Client
	Tokens  TokenSource
}

// NewCheckReporter wires a CheckReporter.
func NewCheckReporter(baseURL string, hc *http.Client, tokens TokenSource) *CheckReporter {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	return &CheckReporter{BaseURL: baseURL, HTTP: hc, Tokens: tokens}
}

type checkRun struct {
	ID         int64  `json:"id"`
	ExternalID string `json:"external_id"`
	Status     string `json:"status"`
}

// Report implements report.CheckReporter.
func (r *CheckReporter) Report(ctx context.Context, run model.RunRecord, check report.Check) error {
	token, err := r.Tokens.Token(ctx)
	if err != nil {
		return fmt.Errorf("github check: token: %w", err)
	}
	owner, name := run.Event.Repository.Owner, run.Event.Repository.Name
	if owner == "" || name == "" {
		return fmt.Errorf("github check: run %s has no repository", run.ID)
	}
	if run.Event.SHA == "" {
		return fmt.Errorf("github check: run %s has no head sha", run.ID)
	}

	existing, err := r.findByExternalID(ctx, token, owner, name, run.Event.SHA, check.ExternalID)
	if err != nil {
		return err
	}
	body := checkPayload(check, run.Event.SHA)
	if existing != nil {
		if err := r.do(ctx, token, http.MethodPatch,
			fmt.Sprintf("%s/repos/%s/%s/check-runs/%d", r.BaseURL, owner, name, existing.ID), body); err != nil {
			return fmt.Errorf("github check: update %s: %w", check.ExternalID, err)
		}
		return nil
	}
	if err := r.do(ctx, token, http.MethodPost,
		fmt.Sprintf("%s/repos/%s/%s/check-runs", r.BaseURL, owner, name), body); err != nil {
		return fmt.Errorf("github check: create %s: %w", check.ExternalID, err)
	}
	return nil
}

func checkPayload(check report.Check, sha string) map[string]any {
	payload := map[string]any{
		"name":        check.Name,
		"head_sha":    sha,
		"external_id": check.ExternalID,
		"details_url": check.DetailsURL,
		"status":      string(check.Status),
	}
	if check.Status == report.StatusCompleted {
		payload["conclusion"] = string(check.Conclusion)
	}
	if check.StartedAt != nil {
		payload["started_at"] = check.StartedAt.UTC().Format(time.RFC3339)
	}
	if check.CompletedAt != nil {
		payload["completed_at"] = check.CompletedAt.UTC().Format(time.RFC3339)
	}
	return payload
}

// findByExternalID lists the check runs of the head commit and matches the
// forgelet external ID.
func (r *CheckReporter) findByExternalID(ctx context.Context, token, owner, name, sha, externalID string) (*checkRun, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/commits/%s/check-runs?per_page=100", r.BaseURL, owner, name, sha)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("github check: build list request: %w", err)
	}
	resp, err := r.send(req, token)
	if err != nil {
		return nil, fmt.Errorf("github check: list for %s: %w", externalID, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github check: list status %d", resp.StatusCode)
	}
	var out struct {
		Total     int        `json:"total_count"`
		CheckRuns []checkRun `json:"check_runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("github check: decode list response: %w", err)
	}
	for i := range out.CheckRuns {
		if out.CheckRuns[i].ExternalID == externalID {
			return &out.CheckRuns[i], nil
		}
	}
	return nil, nil
}

func (r *CheckReporter) do(ctx context.Context, token, method, url string, payload map[string]any) error {
	buf, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("github check: encode payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("github check: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.send(req, token)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("github check: %s %s status %s", method, redactPath(url), resp.Status)
	}
	return nil
}

func (r *CheckReporter) send(req *http.Request, token string) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	return r.HTTP.Do(req)
}

// redactPath keeps query strings out of error messages.
func redactPath(url string) string {
	for i := 0; i < len(url); i++ {
		if url[i] == '?' {
			return url[:i]
		}
	}
	return url
}
