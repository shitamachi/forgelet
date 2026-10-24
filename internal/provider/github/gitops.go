package github

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shitamachi/forgelet/internal/delivery"
)

// GitOps tokens remain in the control plane and are scoped to the registered
// target repository. They are never passed through the executor credential API.
func (c *Client) deliveryToken(ctx context.Context, t delivery.Target) (string, error) {
	if err := t.Validate(); err != nil {
		return "", err
	}
	return c.installationToken(ctx, tokenKey{installation: t.InstallationID, permission: "gitops", repository: t.Repository})
}
func (c *Client) head(ctx context.Context, t delivery.Target, branch, token string) (string, error) {
	var response struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := c.request(ctx, "GET", "/repos/"+t.Repository+"/git/ref/heads/"+branch, nil, token, nil, &response); err != nil {
		return "", err
	}
	if !validSHA(response.Object.SHA) {
		return "", fmt.Errorf("github: invalid ref response")
	}
	return response.Object.SHA, nil
}
func (c *Client) Snapshot(ctx context.Context, t delivery.Target, revision string) (delivery.Snapshot, error) {
	var result delivery.Snapshot
	token, err := c.deliveryToken(ctx, t)
	if err != nil {
		return result, err
	}
	if revision == "" {
		revision, err = c.head(ctx, t, t.Branch, token)
		if err != nil {
			return result, err
		}
	}
	if !validSHA(revision) {
		return result, fmt.Errorf("github: snapshot requires full SHA")
	}
	var commit struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := c.request(ctx, "GET", "/repos/"+t.Repository+"/git/commits/"+revision, nil, token, nil, &commit); err != nil {
		return result, err
	}
	var file contentEntry
	if err := c.request(ctx, "GET", "/repos/"+t.Repository+"/contents/"+t.Path, url.Values{"ref": {revision}}, token, nil, &file); err != nil {
		return result, err
	}
	if file.Type != "file" || file.Path != t.Path || file.Encoding != "base64" || file.Size > 256<<10 || !validSHA(commit.Tree.SHA) {
		return result, fmt.Errorf("github: invalid GitOps manifest response")
	}
	data, err := base64.StdEncoding.DecodeString(file.Content)
	if err != nil || len(data) > 256<<10 {
		return result, fmt.Errorf("github: invalid GitOps content")
	}
	return delivery.Snapshot{SHA: revision, Tree: commit.Tree.SHA, Content: data}, nil
}
func (c *Client) Prepare(ctx context.Context, r delivery.Record) (string, error) {
	token, err := c.deliveryToken(ctx, r.Target)
	if err != nil {
		return "", err
	}
	if !validSHA(r.Parent) || len(r.Content) > 256<<10 {
		return "", fmt.Errorf("github: invalid prepared delivery")
	}
	var parent struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	base := "/repos/" + r.Target.Repository
	if err := c.request(ctx, "GET", base+"/git/commits/"+r.Parent, nil, token, nil, &parent); err != nil {
		return "", err
	}
	if !validSHA(parent.Tree.SHA) {
		return "", fmt.Errorf("github: missing base tree")
	}
	var tree struct {
		SHA string `json:"sha"`
	}
	body := map[string]any{"base_tree": parent.Tree.SHA, "tree": []map[string]string{{"path": r.Target.Path, "mode": "100644", "type": "blob", "content": string(r.Content)}}}
	if err := c.request(ctx, "POST", base+"/git/trees", nil, token, body, &tree); err != nil {
		return "", err
	}
	if !validSHA(tree.SHA) {
		return "", fmt.Errorf("github: invalid created tree")
	}
	message := "deploy(" + r.Target.Name + "): " + r.Digest + "\n\nforgelet-delivery: " + r.ID
	if r.RollbackOf != "" {
		message = "revert(" + r.Target.Name + "): deployment " + r.RollbackOf + "\n\nforgelet-delivery: " + r.ID
	}
	// Explicit author+committer timestamps make retries create the same Git
	// object even if the first response was lost.
	author := map[string]string{"name": "forgelet", "email": "forgelet@users.noreply.github.com", "date": r.CreatedAt.UTC().Format(time.RFC3339)}
	var commit struct {
		SHA string `json:"sha"`
	}
	if err := c.request(ctx, "POST", base+"/git/commits", nil, token, map[string]any{"message": message, "tree": tree.SHA, "parents": []string{r.Parent}, "author": author, "committer": author}, &commit); err != nil {
		return "", err
	}
	return commit.SHA, nil
}
func (c *Client) Contains(ctx context.Context, t delivery.Target, ancestor, descendant string) (bool, error) {
	if !validSHA(ancestor) || !validSHA(descendant) {
		return false, fmt.Errorf("github: comparison requires full SHAs")
	}
	if ancestor == descendant {
		return true, nil
	}
	token, err := c.deliveryToken(ctx, t)
	if err != nil {
		return false, err
	}
	var comparison struct {
		Status string `json:"status"`
	}
	err = c.request(ctx, "GET", "/repos/"+t.Repository+"/compare/"+ancestor+"..."+descendant, url.Values{"per_page": {"1"}}, token, nil, &comparison)
	return comparison.Status == "ahead" || comparison.Status == "identical", err
}
func (c *Client) Publish(ctx context.Context, r delivery.Record) (delivery.Publication, error) {
	var result delivery.Publication
	token, err := c.deliveryToken(ctx, r.Target)
	if err != nil {
		return result, err
	}
	if !validSHA(r.Commit) || !validSHA(r.Parent) {
		return result, fmt.Errorf("github: publication requires prepared commit")
	}
	base := "/repos/" + r.Target.Repository
	if r.Target.Mode == "direct" {
		head, err := c.head(ctx, r.Target, r.Target.Branch, token)
		if err != nil {
			return result, err
		}
		if head != r.Parent {
			contains, err := c.Contains(ctx, r.Target, r.Commit, head)
			if err != nil {
				return result, err
			}
			if !contains {
				return result, delivery.ErrConflict
			}
			return delivery.Publication{Revision: r.Commit}, nil
		}
		err = c.request(ctx, "PATCH", base+"/git/refs/heads/"+r.Target.Branch, nil, token, map[string]any{"sha": r.Commit, "force": false}, nil)
		if isStatus(err, 409) || isStatus(err, 422) {
			return result, delivery.ErrConflict
		}
		if err != nil {
			return result, err
		}
		return delivery.Publication{Revision: r.Commit}, nil
	}
	branch := "forgelet/" + r.Target.Name + "/" + r.ID
	head, err := c.head(ctx, r.Target, branch, token)
	if isStatus(err, 404) {
		err = c.request(ctx, "POST", base+"/git/refs", nil, token, map[string]string{"ref": "refs/heads/" + branch, "sha": r.Commit}, nil)
		if isStatus(err, 422) {
			head, err = c.head(ctx, r.Target, branch, token)
		} else {
			head = r.Commit
		}
	}
	if err != nil {
		return result, err
	}
	if head != r.Commit {
		return result, delivery.ErrConflict
	}
	owner := strings.SplitN(r.Target.Repository, "/", 2)[0]
	var prs []struct {
		Number int    `json:"number"`
		URL    string `json:"html_url"`
		Head   struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	query := url.Values{"state": {"all"}, "head": {owner + ":" + branch}, "base": {r.Target.Branch}, "per_page": {"100"}}
	if err := c.request(ctx, "GET", base+"/pulls", query, token, nil, &prs); err != nil {
		return result, err
	}
	if len(prs) > 1 {
		return result, delivery.ErrConflict
	}
	if len(prs) == 1 {
		if prs[0].Head.SHA != r.Commit {
			return result, delivery.ErrConflict
		}
		return delivery.Publication{PR: prs[0].Number, URL: prs[0].URL}, nil
	}
	var pr struct {
		Number int    `json:"number"`
		URL    string `json:"html_url"`
	}
	err = c.request(ctx, "POST", base+"/pulls", nil, token, map[string]any{"title": "deploy(" + r.Target.Name + "): " + r.Digest, "head": branch, "base": r.Target.Branch, "body": "forgelet delivery `" + r.ID + "` from run `" + r.RunID + "`.\n\nImage: `" + r.Target.Image + "@" + r.Digest + "`"}, &pr)
	if err != nil {
		return result, err
	}
	if pr.Number <= 0 {
		return result, fmt.Errorf("github: invalid delivery PR")
	}
	return delivery.Publication{PR: pr.Number, URL: pr.URL}, nil
}
func (c *Client) PullRequest(ctx context.Context, r delivery.Record) (delivery.PullRequest, error) {
	var result delivery.PullRequest
	token, err := c.deliveryToken(ctx, r.Target)
	if err != nil {
		return result, err
	}
	var pr struct {
		Merged bool   `json:"merged"`
		State  string `json:"state"`
		SHA    string `json:"merge_commit_sha"`
		Base   struct {
			Ref string `json:"ref"`
		} `json:"base"`
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := c.request(ctx, http.MethodGet, "/repos/"+r.Target.Repository+"/pulls/"+strconv.Itoa(r.PR), nil, token, nil, &pr); err != nil {
		return result, err
	}
	if pr.Base.Ref != r.Target.Branch || pr.Head.SHA != r.Commit {
		return result, delivery.ErrConflict
	}
	return delivery.PullRequest{Merged: pr.Merged, Closed: pr.State == "closed", Revision: pr.SHA}, nil
}
