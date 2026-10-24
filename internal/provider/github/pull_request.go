package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/shitamachi/forgelet/internal/run"
)

// ErrComparisonUnsupported is permanent for this event. Retrying cannot make a
// truncated diff complete or create the missing immutable base commit.
var ErrComparisonUnsupported = errors.New("github: unsupported changed-file comparison")

// ParsePullRequest accepts only same-repository PRs and a concrete test merge
// commit. Fork URLs and head SHAs never substitute for a missing merge commit.
func ParsePullRequest(body []byte) (run.Event, error) {
	var p struct {
		Action     string `json:"action"`
		Number     int    `json:"number"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
		PullRequest struct {
			MergeCommitSHA string `json:"merge_commit_sha"`
			Head           struct {
				SHA  string `json:"sha"`
				Repo struct {
					FullName string `json:"full_name"`
					Fork     bool   `json:"fork"`
				} `json:"repo"`
			} `json:"head"`
			Base struct {
				SHA  string `json:"sha"`
				Ref  string `json:"ref"`
				Repo struct {
					FullName string `json:"full_name"`
				} `json:"repo"`
			} `json:"base"`
		} `json:"pull_request"`
	}
	if len(body) > maxResponseBytes || json.Unmarshal(body, &p) != nil {
		return run.Event{}, fmt.Errorf("github: invalid pull request payload")
	}
	switch p.Action {
	case "opened", "synchronize", "reopened", "ready_for_review":
	default:
		return run.Event{}, ErrIgnored
	}
	if p.PullRequest.Head.Repo.Fork || p.PullRequest.Head.Repo.FullName != p.Repository.FullName {
		return run.Event{}, ErrIgnored
	}
	if !validRepository(p.Repository.FullName) || p.PullRequest.Base.Repo.FullName != p.Repository.FullName || p.Installation.ID <= 0 || p.Number <= 0 || !validSHA(p.PullRequest.MergeCommitSHA) || !validSHA(p.PullRequest.Head.SHA) || !validSHA(p.PullRequest.Base.SHA) || p.PullRequest.Base.Ref == "" {
		return run.Event{}, fmt.Errorf("github: pull request requires same-repository identity and a merge commit")
	}
	return run.Event{Name: "pull_request", Action: p.Action, Repository: p.Repository.FullName, SHA: p.PullRequest.MergeCommitSHA, HeadSHA: p.PullRequest.Head.SHA, BeforeSHA: p.PullRequest.Base.SHA, BaseRef: p.PullRequest.Base.Ref, Ref: "refs/pull/" + strconv.Itoa(p.Number) + "/merge", InstallationID: p.Installation.ID, Payload: append(json.RawMessage(nil), body...)}, nil
}

func (c *Client) ChangedFiles(ctx context.Context, event run.Event) ([]string, error) {
	head := event.SHA
	if event.Name == "pull_request" {
		head = event.HeadSHA
	}
	if !validRepository(event.Repository) || !validSHA(event.BeforeSHA) || !validSHA(head) {
		return nil, fmt.Errorf("%w: path filters require a repository and existing immutable base commit", ErrComparisonUnsupported)
	}
	token, err := c.RepositoryToken(ctx, event.InstallationID, event.Repository)
	if err != nil {
		return nil, err
	}
	var response struct {
		Files []struct {
			Filename string `json:"filename"`
			Previous string `json:"previous_filename"`
		} `json:"files"`
	}
	if err := c.request(ctx, http.MethodGet, "/repos/"+event.Repository+"/compare/"+event.BeforeSHA+"..."+head, url.Values{"per_page": {"1"}}, token, nil, &response); err != nil {
		if isStatus(err, http.StatusNotFound) || isStatus(err, http.StatusUnprocessableEntity) {
			return nil, fmt.Errorf("%w: commits cannot be compared: %w", ErrComparisonUnsupported, err)
		}
		return nil, err
	}
	// GitHub caps this field at 300, even when commits are paginated. Reject the
	// boundary rather than silently evaluate filters against a truncated diff.
	if len(response.Files) >= 300 {
		return nil, fmt.Errorf("%w: changed-file list reaches the 300-file API limit", ErrComparisonUnsupported)
	}
	var files []string
	for _, file := range response.Files {
		files = append(files, file.Filename)
		if file.Previous != "" {
			files = append(files, file.Previous)
		}
	}
	return files, nil
}
