package server

import (
	"slices"
	"strings"
	"time"

	gh "github.com/shitamachi/forgelet/internal/provider/github"
)

// GitHubAuthorization is persisted separately from management. Pending hints
// quarantine affected installations without pretending a failed read is a
// completed authoritative revocation or deleting the last successful catalog.
type GitHubAuthorization struct {
	// Identity hints for legacy name-only configuration. These never grant access;
	// they retain an installation association when suspended listings omit repos.
	RepositoryInstallations map[string]int64 `json:"repository_installations,omitempty"`

	DeliveryDigests map[string]string `json:"delivery_digests,omitempty"`
	Version         int64             `json:"version"`
	AttemptedAt     time.Time         `json:"attempted_at,omitzero"`
	CheckedAt       time.Time         `json:"checked_at,omitzero"`
	Error           string            `json:"error,omitempty"`
	Pending         bool              `json:"pending"`
	Quarantined     []int64           `json:"quarantined_installations,omitempty"`
	Deliveries      []string          `json:"recent_deliveries,omitempty"`
	Catalog         *gh.Catalog       `json:"catalog,omitempty"`
}

type RepositoryAuthorization struct {
	State      string `json:"state"`
	RemoteName string `json:"remote_name,omitempty"`
}

func repositoryAuthorization(snapshot *GitHubAuthorization, repo RepositoryConfig, installation int64) RepositoryAuthorization {
	state := func(s string) RepositoryAuthorization { return RepositoryAuthorization{State: s} }
	if snapshot == nil || snapshot.Catalog == nil {
		return state("unknown")
	}
	if repo.InstallationID != 0 && installation != repo.InstallationID {
		return state("not_authorized")
	}
	if slices.Contains(snapshot.Quarantined, installation) {
		return state("pending")
	}
	var inst *gh.Installation
	for i := range snapshot.Catalog.Installations {
		if snapshot.Catalog.Installations[i].ID == installation {
			inst = &snapshot.Catalog.Installations[i]
			break
		}
	}
	if inst == nil {
		return state("not_authorized")
	}
	if inst.SuspendedAt != nil {
		return state("suspended")
	}
	if (inst.Permissions["contents"] != "read" && inst.Permissions["contents"] != "write") || inst.Permissions["checks"] != "write" {
		return state("insufficient_permissions")
	}
	for _, remote := range snapshot.Catalog.Repositories {
		matches := repo.RepositoryID > 0 && remote.ID == repo.RepositoryID || repo.RepositoryID == 0 && strings.EqualFold(remote.Name, repo.Name)
		if !matches || remote.InstallationID != installation {
			continue
		}
		if remote.Archived {
			return state("archived")
		}
		if remote.Disabled {
			return state("disabled")
		}
		if !strings.EqualFold(remote.Name, repo.Name) {
			return RepositoryAuthorization{State: "renamed", RemoteName: remote.Name}
		}
		return state("authorized")
	}
	return state("not_authorized")
}
