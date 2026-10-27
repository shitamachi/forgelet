// Package run defines the durable execution model shared by the control plane and executor.
package run

import (
	"encoding/json"
	"time"
)

// Plan size limits keep one executor claim response below the executor's
// 1 MiB runtime response cap, and bound the total initial plans admitted per run.
const (
	MaxPlanBytes    = 900 << 10
	MaxRunPlanBytes = 16 << 20
)

type State string

const (
	Queued      State = "queued"
	Starting    State = "starting"
	Running     State = "running"
	Succeeded   State = "success"
	Failed      State = "failure"
	Cancelled   State = "cancelled"
	TimedOut    State = "timed_out"
	Interrupted State = "interrupted"
	Skipped     State = "skipped"
)

func (s State) Terminal() bool {
	switch s {
	case Succeeded, Failed, Cancelled, TimedOut, Interrupted, Skipped:
		return true
	}
	return false
}

type Event struct {
	WorkflowPath   string            `json:"workflow_path,omitempty"`
	Action         string            `json:"action,omitempty"`
	BeforeSHA      string            `json:"before_sha,omitempty"`
	HeadSHA        string            `json:"head_sha,omitempty"`
	Name           string            `json:"name,omitempty"`
	BaseRef        string            `json:"base_ref,omitempty"`
	Files          []string          `json:"files,omitempty"`
	FilesKnown     bool              `json:"files_known,omitempty"`
	Inputs         map[string]any    `json:"inputs,omitempty"`
	Vars           map[string]string `json:"vars,omitempty"`
	DeliveryID     string            `json:"delivery_id"`
	Repository     string            `json:"repository"`
	SHA            string            `json:"sha"`
	Ref            string            `json:"ref"`
	InstallationID int64             `json:"installation_id"`
	Payload        json.RawMessage   `json:"payload,omitempty"`
	Error          string            `json:"error,omitempty"`
}

type Run struct {
	Concurrency      string    `json:"concurrency,omitempty"`
	CancelInProgress bool      `json:"cancel_in_progress,omitempty"`
	ID               string    `json:"id"`
	DeliveryID       string    `json:"delivery_id"`
	Repository       string    `json:"repository"`
	SHA              string    `json:"sha"`
	Ref              string    `json:"ref"`
	WorkflowPath     string    `json:"workflow_path"`
	InstallationID   int64     `json:"installation_id"`
	State            State     `json:"state"`
	CreatedAt        time.Time `json:"created_at"`
	Jobs             []Job     `json:"jobs,omitempty"`
}

type Checkout struct {
	Path       string `json:"path,omitempty"`
	FetchDepth int    `json:"fetch_depth"`
}

type Action struct {
	Ref        string            `json:"ref,omitempty"`
	Uses       string            `json:"uses"`
	Repository string            `json:"repository,omitempty"`
	SHA        string            `json:"sha,omitempty"`
	Path       string            `json:"path,omitempty"`
	Inputs     map[string]string `json:"inputs,omitempty"`
}

type Step struct {
	Action            *Action           `json:"action,omitempty"`
	ContinueOnErrorIf string            `json:"continue_on_error_if,omitempty"`
	If                string            `json:"if,omitempty"`
	ContinueOnError   bool              `json:"continue_on_error,omitempty"`
	ID                string            `json:"id"`
	Name              string            `json:"name,omitempty"`
	Run               string            `json:"run,omitempty"`
	Shell             string            `json:"shell,omitempty"`
	WorkingDirectory  string            `json:"working_directory,omitempty"`
	Env               map[string]string `json:"env,omitempty"`
	TimeoutSeconds    int               `json:"timeout_seconds,omitempty"`
	Checkout          *Checkout         `json:"checkout,omitempty"`
}

type Dependency struct {
	Result  State             `json:"result"`
	Outputs map[string]string `json:"outputs,omitempty"`
}

type CallInput struct {
	Template bool   `json:"template,omitempty"`
	Type     string `json:"type"`
	Value    any    `json:"value"`
}
type Plan struct {
	PackageTokenSecret string               `json:"package_token_secret,omitempty"`
	GitHubAPIURL       string               `json:"github_api_url,omitempty"`
	ActionPins         map[string]string    `json:"action_pins,omitempty"`
	Kernel             string               `json:"kernel,omitempty"`
	CallValues         map[string]any       `json:"call_values,omitempty"`
	Control            string               `json:"control,omitempty"`
	ParentGate         string               `json:"parent_gate,omitempty"`
	CallInputs         map[string]CallInput `json:"call_inputs,omitempty"`
	NeedsAliases       map[string]string    `json:"needs_aliases,omitempty"`
	SecretBindings     map[string]string    `json:"secret_bindings,omitempty"`
	RestrictSecrets    bool                 `json:"restrict_secrets,omitempty"`
	RequiredSecrets    []string             `json:"required_secrets,omitempty"`
	// nil means default capabilities; an empty map explicitly denies them.
	Permissions              map[string]string     `json:"permissions"`
	SourceWorkflow           string                `json:"source_workflow,omitempty"`
	WorkflowName             string                `json:"workflow_name,omitempty"`
	Group                    string                `json:"group,omitempty"`
	Matrix                   map[string]any        `json:"matrix,omitempty"`
	MatrixIndex              int                   `json:"matrix_index,omitempty"`
	MatrixTotal              int                   `json:"matrix_total,omitempty"`
	MaxParallel              int                   `json:"max_parallel,omitempty"`
	FailFast                 bool                  `json:"fail_fast,omitempty"`
	ContinueOnError          bool                  `json:"continue_on_error,omitempty"`
	Concurrency              string                `json:"concurrency,omitempty"`
	CancelInProgress         bool                  `json:"cancel_in_progress,omitempty"`
	WorkflowConcurrency      string                `json:"workflow_concurrency,omitempty"`
	WorkflowCancelInProgress bool                  `json:"workflow_cancel_in_progress,omitempty"`
	EventName                string                `json:"event_name,omitempty"`
	BaseRef                  string                `json:"base_ref,omitempty"`
	Inputs                   map[string]any        `json:"inputs,omitempty"`
	Vars                     map[string]string     `json:"vars,omitempty"`
	SecretNames              []string              `json:"secret_names,omitempty"`
	RuntimeSecrets           map[string]string     `json:"-"`
	ActionInputs             map[string]string     `json:"-"`
	ActionPath               string                `json:"-"`
	Key                      string                `json:"key,omitempty"`
	Needs                    []string              `json:"needs,omitempty"`
	If                       string                `json:"if,omitempty"`
	Outputs                  map[string]string     `json:"outputs,omitempty"`
	NeedsContext             map[string]Dependency `json:"needs_context,omitempty"`
	AncestorFailed           bool                  `json:"ancestor_failed,omitempty"`
	JobID                    string                `json:"job_id"`
	RunID                    string                `json:"run_id"`
	Name                     string                `json:"name"`
	Profile                  string                `json:"profile"`
	Repository               string                `json:"repository"`
	SHA                      string                `json:"sha"`
	Ref                      string                `json:"ref"`
	Event                    json.RawMessage       `json:"event,omitempty"`
	Env                      map[string]string     `json:"env,omitempty"`
	Steps                    []Step                `json:"steps"`
	TimeoutSeconds           int                   `json:"timeout_seconds"`
}

type Job struct {
	CheckError      string    `json:"check_error,omitempty"`
	RuntimeFailures int       `json:"runtime_failures,omitempty"`
	RuntimeError    string    `json:"runtime_error,omitempty"`
	RuntimeRetryAt  time.Time `json:"runtime_retry_at,omitzero"`
	LogsExpired     bool      `json:"logs_expired,omitempty"`
	ID              string    `json:"id"`
	RunID           string    `json:"run_id"`
	Name            string    `json:"name"`
	Profile         string    `json:"profile"`
	PodName         string    `json:"pod_name"`
	PodUID          string    `json:"pod_uid,omitempty"`
	State           State     `json:"state"`
	CancelRequested bool      `json:"cancel_requested"`
	Plan            Plan      `json:"plan"`
	Result          *Result   `json:"result,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type StepResult struct {
	Outcome  State             `json:"outcome,omitempty"`
	ID       string            `json:"id"`
	ExitCode int               `json:"exit_code"`
	State    State             `json:"state"`
	Outputs  map[string]string `json:"outputs,omitempty"`
}

type Result struct {
	Outputs    map[string]string `json:"outputs,omitempty"`
	State      State             `json:"state"`
	Error      string            `json:"error,omitempty"`
	Steps      []StepResult      `json:"steps,omitempty"`
	LastLogSeq int64             `json:"last_log_seq"`
}

type Log struct {
	Seq    int64  `json:"seq"`
	StepID string `json:"step_id"`
	Data   string `json:"data"`
}

type Check struct {
	Job      Job
	Run      Run
	RemoteID int64
}
