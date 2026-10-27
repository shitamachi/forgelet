// Package delivery owns durable GitOps intent and deployment reconciliation.
package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/shitamachi/forgelet/internal/blob"
	"github.com/shitamachi/forgelet/internal/run"
)

var ErrConflict = errors.New("GitOps branch or image changed")
var ErrNotFound = errors.New("delivery not found")
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
var shaPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)
var jobPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

type Target struct {
	OnSuccess        *OnSuccess `yaml:"on_success,omitempty" json:"on_success,omitempty"`
	GitURL           string     `yaml:"git_url,omitempty" json:"git_url,omitempty"`
	Name             string     `yaml:"name" json:"name"`
	SourceRepository string     `yaml:"source_repository" json:"source_repository"`
	SourceRef        string     `yaml:"source_ref" json:"source_ref"`
	Repository       string     `yaml:"repository" json:"repository"`
	InstallationID   int64      `yaml:"installation_id" json:"installation_id"`
	Branch           string     `yaml:"branch" json:"branch"`
	Path             string     `yaml:"path" json:"path"`
	Mode             string     `yaml:"mode" json:"mode"`
	Image            string     `yaml:"image" json:"image"`
	ImageName        string     `yaml:"image_name" json:"image_name"`
	FluxNamespace    string     `yaml:"flux_namespace" json:"flux_namespace"`
	Kustomization    string     `yaml:"kustomization" json:"kustomization"`
	Namespace        string     `yaml:"namespace" json:"namespace"`
	Deployment       string     `yaml:"deployment" json:"deployment"`
	Container        string     `yaml:"container" json:"container"`
}

// OnSuccess binds an original workflow publisher to a native deployment job.
type OnSuccess struct {
	WorkflowPath string `yaml:"workflow_path" json:"workflow_path"`
	Job          string `yaml:"job" json:"job"`
	Profile      string `yaml:"profile" json:"profile"`
}

func (t Target) Validate() error {
	if t.OnSuccess != nil {
		a := t.OnSuccess
		if !blob.SafePath(a.WorkflowPath) || !strings.HasPrefix(a.WorkflowPath, ".github/workflows/") || strings.Count(a.WorkflowPath, "/") != 2 || (!strings.HasSuffix(a.WorkflowPath, ".yml") && !strings.HasSuffix(a.WorkflowPath, ".yaml")) || !jobPattern.MatchString(a.Job) || a.Profile == "" {
			return fmt.Errorf("automatic deployment requires a workflow path, job and native profile")
		}
	}
	if t.GitURL != "" {
		u, err := url.Parse(t.GitURL)
		if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "ssh") || (u.User != nil && u.Scheme != "ssh") {
			return fmt.Errorf("git_url requires HTTPS or SSH without credentials")
		}
		if u.User != nil {
			if _, password := u.User.Password(); password {
				return fmt.Errorf("git_url cannot contain a password")
			}
		}
	}
	for _, name := range []string{t.Name, t.FluxNamespace, t.Kustomization, t.Namespace, t.Deployment, t.Container} {
		if !namePattern.MatchString(name) {
			return fmt.Errorf("invalid deployment target resource name")
		}
	}
	if !repoPattern.MatchString(t.SourceRepository) || !repoPattern.MatchString(t.Repository) || t.InstallationID <= 0 || !strings.HasPrefix(t.SourceRef, "refs/heads/") {
		return fmt.Errorf("target requires source repository/branch and GitHub installation/repository")
	}
	if t.Branch == "" || strings.ContainsAny(t.Branch, " ~^:?*[\\\x00\r\n") || strings.Contains(t.Branch, "..") || strings.HasSuffix(t.Branch, "/") || strings.Contains(t.Branch, "@{") {
		return fmt.Errorf("invalid target branch")
	}
	if !blob.SafePath(t.Path) || strings.HasPrefix(t.Path, ".github/") || (!strings.HasSuffix(t.Path, ".yaml") && !strings.HasSuffix(t.Path, ".yml")) {
		return fmt.Errorf("target path must be a registered Kustomize YAML file")
	}
	if (t.Mode != "direct" && t.Mode != "pr") || !run.ValidImage(t.Image) || t.ImageName == "" {
		return fmt.Errorf("target requires direct/pr mode and image mapping")
	}
	return nil
}

func (t Target) SourceURL() string {
	if t.GitURL != "" {
		return t.GitURL
	}
	return "https://github.com/" + t.Repository + ".git"
}

type State string

const (
	Queued        State = "queued"
	Preparing     State = "preparing"
	AwaitingMerge State = "awaiting_merge"
	Delivered     State = "delivered"
	Deployed      State = "deployed"
	Superseded    State = "superseded"
	Cancelled     State = "cancelled"
	Conflict      State = "conflict"
)

type Record struct {
	ID               string    `json:"id"`
	Target           Target    `json:"target"`
	RunID            string    `json:"run_id"`
	JobID            string    `json:"job_id"`
	Digest           string    `json:"digest"`
	State            State     `json:"state"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	Parent           string    `json:"parent,omitempty"`
	Commit           string    `json:"commit,omitempty"`
	Revision         string    `json:"revision,omitempty"`
	ObservedRevision string    `json:"observed_revision,omitempty"`
	PreviousDigest   string    `json:"previous_digest,omitempty"`
	Content          []byte    `json:"content,omitempty"`
	PR               int       `json:"pr,omitempty"`
	URL              string    `json:"url,omitempty"`
	RollbackOf       string    `json:"rollback_of,omitempty"`
	ExpectedDigest   string    `json:"expected_digest,omitempty"`
	Error            string    `json:"error,omitempty"`
	Version          int       `json:"version"`
}

func ID(runID, target string) string {
	h := sha256.Sum256([]byte(runID + "\x00" + target))
	return hex.EncodeToString(h[:16])
}
func (r Record) Active() bool {
	return r.State == Queued || r.State == Preparing || r.State == AwaitingMerge || r.State == Delivered || r.State == Deployed
}

type Snapshot struct {
	SHA, Tree string
	Content   []byte
}
type Publication struct {
	PR            int
	URL, Revision string
}
type PullRequest struct {
	Merged, Closed bool
	Revision       string
}
type Observation struct {
	Revision, Image string
	Ready           bool
}
type Repository interface {
	Snapshot(context.Context, Target, string) (Snapshot, error)
	Prepare(context.Context, Record) (string, error)
	Publish(context.Context, Record) (Publication, error)
	PullRequest(context.Context, Record) (PullRequest, error)
	Contains(context.Context, Target, string, string) (bool, error)
}
type Observer interface {
	Observe(context.Context, Target) (Observation, error)
}
type Store interface {
	EnqueueDelivery(context.Context, Record) (Record, error)
	SaveDelivery(context.Context, Record) error
	GetDelivery(context.Context, string) (Record, error)
	Deliveries(context.Context, bool) ([]Record, error)
	GetRun(context.Context, string) (run.Run, error)
}
