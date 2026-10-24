package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/shitamachi/forgelet/internal/run"
)

type remoteCheck struct {
	ID         int64  `json:"id"`
	ExternalID string `json:"external_id"`
	HeadSHA    string `json:"head_sha"`
	App        struct {
		ID int64 `json:"id"`
	} `json:"app"`
}

// ReportCheck creates or updates this App's check. Callers persist the returned
// ID in their outbox. Before creating it always searches by SHA and external_id,
// allowing an outbox retry to recover a POST whose response was lost.
func (c *Client) ReportCheck(ctx context.Context, check run.Check, detailsURL string) (int64, error) {
	if !validRepository(check.Run.Repository) || !validSHA(check.Run.SHA) || check.Job.ID == "" || check.RemoteID < 0 {
		return 0, errors.New("github: invalid check identity")
	}
	if detailsURL != "" {
		u, err := url.Parse(detailsURL)
		if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
			return 0, errors.New("github: invalid check details URL")
		}
	}
	status, conclusion, err := checkStatus(check.Job.State)
	if err != nil {
		return 0, err
	}
	token, err := c.installationToken(ctx, tokenKey{installation: check.Run.InstallationID, permission: "checks", repository: check.Run.Repository})
	if err != nil {
		return 0, err
	}
	base := "/repos/" + check.Run.Repository
	id := check.RemoteID
	if id == 0 {
		id, err = c.findCheck(ctx, base, token, check.Run.SHA, check.Job.ID)
		if err != nil {
			return 0, fmt.Errorf("github: find check: %w", err)
		}
	} else {
		var existing remoteCheck
		if err := c.request(ctx, http.MethodGet, base+"/check-runs/"+strconv.FormatInt(id, 10), nil, token, nil, &existing); err != nil {
			return 0, fmt.Errorf("github: read check: %w", err)
		}
		if existing.ID != id || existing.ExternalID != check.Job.ID || existing.HeadSHA != check.Run.SHA || existing.App.ID != c.appID {
			return 0, errors.New("github: check identity or App mismatch")
		}
	}
	name := check.Job.Name
	if name == "" {
		name = check.Job.ID
	}
	body := map[string]any{"name": "forgelet / " + name, "status": status, "external_id": check.Job.ID, "details_url": detailsURL}
	if status == "in_progress" {
		started := check.Job.CreatedAt
		if started.IsZero() {
			started = c.now()
		}
		body["started_at"] = started.UTC().Format(time.RFC3339)
	}
	if conclusion != "" {
		body["conclusion"] = conclusion
		completed := check.Job.UpdatedAt
		if completed.IsZero() {
			completed = c.now()
		}
		body["completed_at"] = completed.UTC().Format(time.RFC3339)
		body["output"] = map[string]string{"title": "Forgelet: " + string(check.Job.State), "summary": "Execution state: " + string(check.Job.State) + ". Open details for logs."}
	}
	method, endpoint := http.MethodPatch, base+"/check-runs/"+strconv.FormatInt(id, 10)
	if id == 0 {
		method, endpoint = http.MethodPost, base+"/check-runs"
		body["head_sha"] = check.Run.SHA
	}
	var response remoteCheck
	if err := c.request(ctx, method, endpoint, nil, token, body, &response); err != nil {
		return 0, fmt.Errorf("github: report check: %w", err)
	}
	if response.ID <= 0 || (id != 0 && response.ID != id) || response.App.ID != c.appID || response.ExternalID != check.Job.ID || response.HeadSHA != check.Run.SHA {
		return 0, errors.New("github: invalid check response identity")
	}
	return response.ID, nil
}

func checkStatus(state run.State) (string, string, error) {
	switch state {
	case run.Queued, run.Starting:
		return "queued", "", nil
	case run.Running:
		return "in_progress", "", nil
	case run.Skipped:
		return "completed", "skipped", nil
	case run.Succeeded:
		return "completed", "success", nil
	case run.Failed:
		return "completed", "failure", nil
	case run.Cancelled:
		return "completed", "cancelled", nil
	case run.TimedOut:
		return "completed", "timed_out", nil
	case run.Interrupted:
		return "completed", "action_required", nil
	default:
		return "", "", errors.New("github: unsupported check state")
	}
}

func (c *Client) findCheck(ctx context.Context, base, token, sha, externalID string) (int64, error) {
	for page := 1; page <= 100; page++ {
		var response struct {
			CheckRuns []remoteCheck `json:"check_runs"`
		}
		query := url.Values{"filter": {"all"}, "per_page": {"100"}, "page": {strconv.Itoa(page)}, "app_id": {strconv.FormatInt(c.appID, 10)}}
		if err := c.request(ctx, http.MethodGet, base+"/commits/"+sha+"/check-runs", query, token, nil, &response); err != nil {
			return 0, err
		}
		for _, existing := range response.CheckRuns {
			if existing.ID > 0 && existing.ExternalID == externalID && existing.HeadSHA == sha && existing.App.ID == c.appID {
				return existing.ID, nil
			}
		}
		if len(response.CheckRuns) < 100 {
			return 0, nil
		}
	}
	return 0, errors.New("github: check lookup exceeds pagination limit")
}
