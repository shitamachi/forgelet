package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/shitamachi/forgelet/internal/run"
)

// ErrComparisonUnsupported is permanent for this event. Retrying cannot make a
// truncated diff complete or create the missing immutable base commit.
var ErrComparisonUnsupported = errors.New("github: unsupported changed-file comparison")

type pullRequestIdentity struct {
	SHA  string `json:"sha"`
	Ref  string `json:"ref"`
	Repo struct {
		FullName string `json:"full_name"`
		Fork     bool   `json:"fork"`
	} `json:"repo"`
}
type pullRequestDetails struct {
	Number         int                 `json:"number"`
	State          string              `json:"state"`
	Mergeable      *bool               `json:"mergeable"`
	MergeCommitSHA string              `json:"merge_commit_sha"`
	Head           pullRequestIdentity `json:"head"`
	Base           pullRequestIdentity `json:"base"`
}

// ParsePullRequest accepts only same-repository PR identities. GitHub may not
// have computed the test merge yet; an empty SHA is persisted for resolution.
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
		PullRequest pullRequestDetails `json:"pull_request"`
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
	if !validRepository(p.Repository.FullName) || p.PullRequest.Base.Repo.FullName != p.Repository.FullName || p.Installation.ID <= 0 || p.Number <= 0 || (p.PullRequest.MergeCommitSHA != "" && !validSHA(p.PullRequest.MergeCommitSHA)) || !validSHA(p.PullRequest.Head.SHA) || !validSHA(p.PullRequest.Base.SHA) || p.PullRequest.Base.Ref == "" {
		return run.Event{}, fmt.Errorf("github: pull request requires valid same-repository head/base identity")
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

// ErrPullRequestSuperseded cannot be retried into executing different source.
var ErrPullRequestSuperseded = errors.New("github: pull request no longer matches the original event")

// ResolvePullRequest resolves only the original head/base pair. It never falls
// back to the head commit or adopts a later branch update after an API failure.
func (c *Client) ResolvePullRequest(ctx context.Context, event run.Event) (string, error) {
	number := strings.TrimSuffix(strings.TrimPrefix(event.Ref, "refs/pull/"), "/merge")
	n, err := strconv.Atoi(number)
	if err != nil || n < 1 || event.Ref != "refs/pull/"+strconv.Itoa(n)+"/merge" || event.Name != "pull_request" || !validRepository(event.Repository) || !validSHA(event.HeadSHA) || !validSHA(event.BeforeSHA) || event.InstallationID <= 0 {
		return "", fmt.Errorf("%w: invalid PR identity", ErrPullRequestSuperseded)
	}
	token, err := c.installationToken(ctx, tokenKey{installation: event.InstallationID, permission: "pr-merge", repository: event.Repository})
	if err != nil {
		return "", err
	}
	var pr pullRequestDetails
	if err = c.request(ctx, http.MethodGet, "/repos/"+event.Repository+"/pulls/"+number, nil, token, nil, &pr); err != nil {
		return "", err
	}
	if pr.Number != n || pr.State != "open" || pr.Head.Repo.Fork || pr.Head.Repo.FullName != event.Repository || pr.Base.Repo.FullName != event.Repository || pr.Head.SHA != event.HeadSHA || pr.Base.SHA != event.BeforeSHA || pr.Base.Ref != event.BaseRef {
		return "", ErrPullRequestSuperseded
	}
	if pr.Mergeable != nil && !*pr.Mergeable {
		return "", fmt.Errorf("%w: PR has merge conflicts", ErrPullRequestSuperseded)
	}
	if pr.Mergeable == nil {
		return "", fmt.Errorf("github: PR test merge commit is not ready")
	}
	// REST 2026-03-10 no longer returns merge_commit_sha. Resolve the
	// actual test-merge ref, then bind its parents to the original event to
	// close the race between reading PR metadata and reading the mutable ref.
	var ref struct {
		Object struct {
			SHA  string `json:"sha"`
			Type string `json:"type"`
		} `json:"object"`
	}
	if err = c.request(ctx, http.MethodGet, "/repos/"+event.Repository+"/git/ref/pull/"+number+"/merge", nil, token, nil, &ref); err != nil {
		return "", err
	}
	if ref.Object.Type != "commit" || !validSHA(ref.Object.SHA) {
		return "", fmt.Errorf("github: PR merge ref is not ready")
	}
	var commit struct {
		SHA     string `json:"sha"`
		Parents []struct {
			SHA string `json:"sha"`
		} `json:"parents"`
	}
	if err = c.request(ctx, http.MethodGet, "/repos/"+event.Repository+"/git/commits/"+ref.Object.SHA, nil, token, nil, &commit); err != nil {
		return "", err
	}
	if commit.SHA != ref.Object.SHA || len(commit.Parents) != 2 || commit.Parents[0].SHA != event.BeforeSHA || commit.Parents[1].SHA != event.HeadSHA {
		return "", fmt.Errorf("github: PR test merge does not match original parents")
	}
	return ref.Object.SHA, nil
}
