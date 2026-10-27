package actworker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nektos/act/pkg/common"
	"github.com/nektos/act/pkg/model"
	"github.com/nektos/act/pkg/runner"
	"github.com/sirupsen/logrus"

	"github.com/shitamachi/forgelet/internal/run"
)

type formatter struct{}

func (formatter) Format(e *logrus.Entry) ([]byte, error) {
	m := run.ActMessage{Message: e.Message + "\n"}
	if e.Data["stage"] == "Main" && strings.HasPrefix(e.Message, "exitcode '") {
		var code int
		if _, err := fmt.Sscanf(e.Message, "exitcode '%d':", &code); err == nil && code >= 0 && code <= 255 {
			m.ExitCode = &code
		}
	}
	if ids, ok := e.Data["stepID"].([]string); ok && len(ids) > 0 {
		m.StepID = ids[0]
	}
	if e.Data["command"] == "add-mask" {
		m.Mask, _ = e.Data["arg"].(string)
		m.Message = ""
	}
	data, err := json.Marshal(m)
	return append(data, '\n'), err
}

type loggerFactory struct{ out io.Writer }

func (f loggerFactory) WithJobLogger() *logrus.Logger {
	l := logrus.New()
	l.SetOutput(f.out)
	l.SetFormatter(formatter{})
	l.SetLevel(logrus.InfoLevel)
	return l
}

func Run(ctx context.Context, input io.Reader, output io.Writer) error {
	var request run.ActRequest
	dec := json.NewDecoder(io.LimitReader(input, 4<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&request); err != nil {
		return fmt.Errorf("decode job request: %w", err)
	}
	if request.Plan.Kernel != "act" || request.Plan.JobID == "" || request.Image == "" || request.Workspace == "" {
		return fmt.Errorf("invalid act job identity or profile")
	}
	meta, err := os.MkdirTemp("", "forgelet-act-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(meta) }()
	wf, err := workflow(request.Plan)
	if err != nil {
		return err
	}
	configureBuilderDefaults(wf, request)
	secrets := executionSecrets(wf, request)
	env := runtimeEnvironment(request)
	cache := &sources{dir: filepath.Join(meta, "sources"), base: request.ActionBaseURL, pins: request.Plan.ActionPins}
	public, _ := request.Plan.Contexts()
	actor := public["github.actor"]
	if actor == "" {
		actor = "forgelet"
	}
	cfg := &runner.Config{Actor: actor, Workdir: request.Workspace, DefaultBranch: "main", EventName: request.Plan.EventName, GitHubInstance: "github.com", Env: env, Secrets: secrets, Vars: request.Plan.Vars, Token: request.Token,
		Platforms: map[string]string{"forgelet": request.Image}, BindWorkdir: true, ContainerOptions: "--user 0:0 --mount type=bind,source=/tmp,target=/tmp", ContainerDaemonSocket: run.ActSocket, LogOutput: true, NoSkipCheckout: true, AutoRemove: true, ActionCacheDir: filepath.Join(meta, "actions"), ActionCache: cache}
	if cfg.EventName == "" {
		cfg.EventName = "push"
	}
	log := loggerFactory{output}
	ctx = runner.WithJobLoggerFactory(ctx, log)
	rc := &runner.RunContext{Config: cfg, Run: &model.Run{Workflow: wf, JobID: localKey(request.Plan)}, Matrix: request.Plan.Matrix, StepResults: map[string]*model.StepResult{}, EventJSON: string(request.Plan.Event), Name: request.Plan.Key}
	if rc.EventJSON == "" {
		rc.EventJSON = "{}"
	}
	ctx = common.WithJobErrorContainer(runner.WithJobLogger(ctx, request.Plan.JobID, request.Plan.Name, cfg, &rc.Masks, request.Plan.Matrix))
	rc.ExprEval = rc.NewExpressionEvaluator(ctx)
	execute, err := rc.Executor()
	if err != nil {
		return err
	}
	err = execute(ctx)
	result := run.Result{State: run.State(rc.Run.Job().Result), Outputs: rc.Run.Job().Outputs}
	if err != nil {
		result.State = run.Failed
		if !strings.HasPrefix(err.Error(), "exitcode '") {
			result.Error = "act job infrastructure failed"
		}
	}
	if !result.State.Terminal() {
		result.State = run.Failed
		result.Error = "act returned an unknown job result"
	}
	for _, step := range request.Plan.Steps {
		s := rc.StepResults[step.ID]
		if s == nil {
			continue
		}
		code := 0
		if s.Outcome == model.StepStatusFailure {
			code = -1
		}
		result.Steps = append(result.Steps, run.StepResult{ID: step.ID, State: run.State(s.Conclusion.String()), Outcome: run.State(s.Outcome.String()), ExitCode: code, Outputs: s.Outputs})
	}
	// Include masks discovered by post steps before publishing any outputs.
	for _, value := range rc.Masks {
		if err := json.NewEncoder(output).Encode(run.ActMessage{Mask: value}); err != nil {
			return err
		}
	}
	// Deterministic ordering is useful for evidence and protocol tests.
	sort.SliceStable(result.Steps, func(i, j int) bool {
		return index(request.Plan.Steps, result.Steps[i].ID) < index(request.Plan.Steps, result.Steps[j].ID)
	})
	return json.NewEncoder(output).Encode(run.ActMessage{Result: &result})
}
func index(steps []run.Step, id string) int {
	for i, s := range steps {
		if s.ID == id {
			return i
		}
	}
	return len(steps)
}

func runtimeEnvironment(request run.ActRequest) map[string]string {
	env := map[string]string{"GITHUB_REPOSITORY": request.Plan.Repository, "GITHUB_SHA": request.Plan.SHA, "GITHUB_REF": request.Plan.Ref, "GITHUB_RUN_ID": request.Plan.RunID, "GITHUB_RUN_NUMBER": "1", "DOCKER_HOST": "unix:///var/run/docker.sock", "TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE": run.ActSocket, "TESTCONTAINERS_HOST_OVERRIDE": "127.0.0.1"}
	public, _ := request.Plan.Contexts()
	env["GITHUB_REF_TYPE"] = public["github.ref_type"]
	env["GITHUB_REF_NAME"] = public["github.ref_name"]
	if request.Plan.GitHubAPIURL != "" {
		env["GITHUB_API_URL"] = request.Plan.GitHubAPIURL
	}
	if owner, _, ok := strings.Cut(request.Plan.Repository, "/"); ok {
		env["GITHUB_REPOSITORY_OWNER"] = owner
	}
	if request.CacheURL != "" {
		env["ACTIONS_CACHE_URL"] = request.CacheURL
		env["ACTIONS_RESULTS_URL"] = request.CacheURL
		env["ACTIONS_CACHE_SERVICE_V2"] = "true"
		env["ACTIONS_RUNTIME_URL"] = request.CacheURL
		env["ACTIONS_RUNTIME_TOKEN"] = request.CacheToken
	}
	return env
}
