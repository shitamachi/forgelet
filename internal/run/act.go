package run

// ActRequest is an ephemeral pipe message, never a persisted execution plan.
// It deliberately carries no control-plane identity or Kubernetes credential.
type ActRequest struct {
	CPUCount       int64             `json:"cpu_count,omitempty"`
	BuildkitImage  string            `json:"buildkit_image,omitempty"`
	BuildkitConfig string            `json:"buildkit_config,omitempty"`
	Plan           Plan              `json:"plan"`
	Workspace      string            `json:"workspace"`
	Image          string            `json:"image"`
	Secrets        map[string]string `json:"secrets"`
	PackageToken   string            `json:"package_token,omitempty"`
	Token          string            `json:"token"`
	CacheURL       string            `json:"cache_url"`
	CacheToken     string            `json:"cache_token"`
	ActionBaseURL  string            `json:"action_base_url,omitempty"`
}

type ActMessage struct {
	Progress *ProgressEvent `json:"progress,omitempty"`
	ExitCode *int           `json:"exit_code,omitempty"`
	StepID   string         `json:"step_id,omitempty"`
	Message  string         `json:"message,omitempty"`
	Mask     string         `json:"mask,omitempty"`
	Result   *Result        `json:"result,omitempty"`
}

const ActSocket = "/run/forgelet-act/docker.sock"

// Preparing the administrator's execution environment is bounded separately
// from the workflow job's timeout. No workflow steps run during this phase.
const ActPreparationSeconds = 15 * 60
