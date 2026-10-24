package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Catalog contains metadata only. Credentials never cross the provider boundary.
type Catalog struct {
	App           string         `json:"app"`
	InstallURL    string         `json:"install_url"`
	Installations []Installation `json:"installations"`
	Repositories  []Repository   `json:"repositories"`
	CheckedAt     time.Time      `json:"checked_at"`
}
type Installation struct {
	ID      int64 `json:"id"`
	Account struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"account"`
	Selection   string            `json:"repository_selection"`
	Permissions map[string]string `json:"permissions"`
	SuspendedAt *time.Time        `json:"suspended_at"`
	ManageURL   string            `json:"manage_url"`
}
type Repository struct {
	ID             int64  `json:"id"`
	Name           string `json:"full_name"`
	InstallationID int64  `json:"installation_id"`
	DefaultBranch  string `json:"default_branch"`
	Private        bool   `json:"private"`
	Archived       bool   `json:"archived"`
	Disabled       bool   `json:"disabled"`
}

var appSlug = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// Discover enumerates only installations owned by this App, and publishes no
// partial result if pagination or any installation fails. Limits bound API work.
func (c *Client) Discover(ctx context.Context) (Catalog, error) {
	fail := func(err error) (Catalog, error) { return Catalog{}, fmt.Errorf("github: discover: %w", err) }
	jwt, err := c.jwt()
	if err != nil {
		return fail(err)
	}
	var app struct {
		ID   int64  `json:"id"`
		Slug string `json:"slug"`
	}
	if err = c.request(ctx, http.MethodGet, "/app", nil, jwt, nil, &app); err != nil {
		return fail(err)
	}
	if app.ID != c.appID || !appSlug.MatchString(app.Slug) {
		return fail(errors.New("invalid App identity"))
	}
	// Enterprise uses its configured origin rather than URLs supplied by API data.
	origin := "https://github.com"
	if c.baseURL.Hostname() != "api.github.com" && c.baseURL.Hostname() != "127.0.0.1" && c.baseURL.Hostname() != "localhost" {
		origin = c.baseURL.Scheme + "://" + c.baseURL.Host
	}
	out := Catalog{App: app.Slug, InstallURL: origin + "/apps/" + app.Slug + "/installations/new", Installations: []Installation{}, Repositories: []Repository{}, CheckedAt: c.now().UTC()}
	seenInst := map[int64]bool{}
	seenRepo := map[int64]bool{}
	for page := 1; ; page++ {
		if page > 10 {
			return fail(errors.New("installation listing limit exceeded"))
		}
		var batch []Installation
		if err = c.request(ctx, http.MethodGet, "/app/installations", pageQuery(page), jwt, nil, &batch); err != nil {
			return fail(err)
		}
		for _, inst := range batch {
			if inst.ID <= 0 || seenInst[inst.ID] || !appSlug.MatchString(inst.Account.Login) {
				return fail(errors.New("invalid installation metadata"))
			}
			seenInst[inst.ID] = true
			inst.ManageURL = origin + "/settings/installations/" + strconv.FormatInt(inst.ID, 10)
			if inst.Account.Type == "Organization" {
				inst.ManageURL = origin + "/organizations/" + inst.Account.Login + "/settings/installations/" + strconv.FormatInt(inst.ID, 10)
			}
			out.Installations = append(out.Installations, inst)
			if inst.SuspendedAt != nil {
				continue
			}
			// A fresh metadata token makes changed repository grants visible immediately.
			key := tokenKey{installation: inst.ID, permission: "metadata"}
			c.mu.Lock()
			delete(c.tokens, key)
			c.mu.Unlock()
			token, e := c.installationToken(ctx, key)
			if e != nil {
				return fail(e)
			}
			for rp := 1; ; rp++ {
				if rp > 100 {
					return fail(errors.New("repository listing limit exceeded"))
				}
				var result struct {
					Total        int          `json:"total_count"`
					Repositories []Repository `json:"repositories"`
				}
				if e = c.request(ctx, http.MethodGet, "/installation/repositories", pageQuery(rp), token, nil, &result); e != nil {
					return fail(e)
				}
				for _, repo := range result.Repositories {
					if repo.ID <= 0 || repo.ID > 9007199254740991 || !validRepository(repo.Name) || seenRepo[repo.ID] {
						return fail(errors.New("invalid or changing repository listing; refresh again"))
					}
					seenRepo[repo.ID] = true
					repo.InstallationID = inst.ID
					out.Repositories = append(out.Repositories, repo)
					if len(out.Repositories) > 10000 {
						return fail(errors.New("repository listing limit exceeded"))
					}
				}
				if len(result.Repositories) < 100 {
					break
				}
			}
		}
		if len(batch) < 100 {
			break
		}
	}
	slices.SortFunc(out.Repositories, func(a, b Repository) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}
func pageQuery(page int) url.Values {
	return url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}
}

// DefaultCommit resolves the default branch once; subsequent reads use its SHA.
func (c *Client) DefaultCommit(ctx context.Context, repo Repository) (string, error) {
	if !validRepository(repo.Name) || repo.DefaultBranch == "" || strings.ContainsAny(repo.DefaultBranch, "\x00\r\n") {
		return "", errors.New("github: repository has no default branch")
	}
	token, err := c.RepositoryToken(ctx, repo.InstallationID, repo.Name)
	if err != nil {
		return "", err
	}
	var result []struct {
		SHA string `json:"sha"`
	}
	if err = c.request(ctx, http.MethodGet, "/repos/"+repo.Name+"/commits", url.Values{"sha": {repo.DefaultBranch}, "per_page": {"1"}}, token, nil, &result); err != nil {
		return "", fmt.Errorf("github: resolve default branch: %w", err)
	}
	if len(result) != 1 || !validSHA(result[0].SHA) {
		return "", errors.New("github: repository has no valid default commit")
	}
	return result[0].SHA, nil
}
