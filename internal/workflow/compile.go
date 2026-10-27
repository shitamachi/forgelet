// Package workflow validates the deliberately small workflow subset understood by forgelet.
// Compilation is deterministic and performs no network or filesystem operations.
package workflow

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/rhysd/actionlint"
	"go.yaml.in/yaml/v4"

	"github.com/shitamachi/forgelet/internal/action"
	"github.com/shitamachi/forgelet/internal/expression"
	"github.com/shitamachi/forgelet/internal/run"
)

const maxSourceBytes = 1 << 20

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
var envIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Compile returns job plans in job-key order with validated acyclic dependencies. Runtime interpolation is
// validated here and retained for the executor, after file commands have applied.
func Compile(source []byte, event run.Event, runID string) ([]run.Plan, error) {
	return CompileFiles(source, event, runID, nil)
}

// Validate checks job semantics even when the supplied event does not match filters.
// Execution still uses Compile and must satisfy all original triggers.
func Validate(source []byte, event run.Event) ([]run.Plan, error) {
	return ValidateFiles(source, event, nil)
}
func (c *bundleCompiler) compile(source []byte, event run.Event, runID string, validating bool, scope callScope) ([]run.Plan, error) {
	if runID == "" {
		return nil, fmt.Errorf("workflow run ID is required")
	}
	doc, err := workflowDocument(source)
	if err != nil {
		return nil, err
	}
	_, parseErrors := actionlint.Parse(source)
	root, err := mapping(doc.Content[0], "name", "on", "env", "jobs", "concurrency", "permissions", "defaults", "run-name")
	if err != nil {
		return nil, err
	}
	permissions, err := compilePermissions(root["permissions"], scope.permissions)
	if err == nil {
		err = permissionCeiling(permissions, scope.permissions)
	}
	if err != nil {
		return nil, err
	}
	if root["run-name"] != nil {
		return nil, diagnostic(root["run-name"], "execution capability run-name is not implemented")
	}
	if validating {
		if err := validateDispatchDefinition(root["on"]); err != nil {
			return nil, err
		}
	}
	event, err = compileTrigger(root["on"], event)
	if validating && (errors.Is(err, ErrNotTriggered) || errors.Is(err, ErrFilesRequired)) {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	workflowName, err := optionalString(root["name"], "")
	if err != nil {
		return nil, err
	}
	if scope.gate != "" && root["concurrency"] != nil {
		return nil, diagnostic(root["concurrency"], "called workflow concurrency is not implemented")
	}
	if scope.gate != "" {
		workflowName = scope.workflowName
	}
	if workflowName == "" {
		workflowName = event.WorkflowPath
	}
	if strings.Contains(workflowName, "${{") {
		return nil, diagnostic(root["name"], "workflow name must be static")
	}
	workflowEnv, err := environment(root["env"], referenceScope{})
	if err != nil {
		return nil, err
	}
	jobs, err := mapping(root["jobs"])
	if err != nil {
		return nil, fmt.Errorf("jobs: %w", err)
	}
	if len(jobs) == 0 || len(jobs) > 64 {
		return nil, diagnostic(root["jobs"], "workflow must have between 1 and 64 jobs")
	}
	keys := sortedKeys(jobs)
	dependencies, err := compileDependencies(jobs)
	if err != nil {
		return nil, err
	}
	plans := make([]run.Plan, 0, len(keys))
	for _, key := range keys {
		if !identifier.MatchString(key) {
			return nil, diagnostic(jobs[key], "invalid job identifier %q", key)
		}
		fields, err := mapping(jobs[key])
		if err != nil {
			return nil, err
		}
		if fields["uses"] != nil {
			called, callErr := c.compileCall(jobs[key], key, workflowName, dependencies[key], event, runID, validating, scope, permissions)
			if callErr != nil {
				return nil, fmt.Errorf("job %s: %w", key, callErr)
			}
			plans = append(plans, called...)
			continue
		}
		for _, earlyField := range []string{"runs-on", "name", "continue-on-error", "concurrency"} {
			if node := fields[earlyField]; node != nil {
				data, _ := yaml.Marshal(node)
				var deferred string
				_ = expression.ValidateTemplate(string(data), func(ref string) bool {
					if (ref == "inputs" || strings.HasPrefix(ref, "inputs.")) && runtimeCallReference(ref, scope.deferred) {
						deferred = ref
					}
					return true
				})
				if deferred != "" {
					return nil, diagnostic(node, "%s depends on runtime input %s; deferred execution settings are not implemented", earlyField, deferred)
				}
			}
		}
		strategy, err := compileStrategy(fields["strategy"])
		if err != nil {
			return nil, fmt.Errorf("job %s: %w", key, err)
		}
		for index, matrix := range strategy.values {
			plan, err := compileJob(jobs[key], key, workflowName, workflowEnv, dependencies[key], event, runID, matrix)
			if err != nil {
				return nil, fmt.Errorf("job %s: %w", key, err)
			}
			plan.Group = key
			if fields["strategy"] != nil {
				plan.Key = fmt.Sprintf("%s[%d]", key, index)
				sum := sha256.Sum256([]byte(runID + "\x00" + plan.Key))
				plan.JobID = hex.EncodeToString(sum[:])[:24]
				plan.Matrix = matrix
				plan.MatrixIndex = index
				plan.MatrixTotal = len(strategy.values)
				plan.MaxParallel = strategy.parallel
				plan.FailFast = strategy.failFast
				plan.Name += fmt.Sprintf(" [%d]", index)
			}
			plan.WorkflowConcurrency, plan.WorkflowCancelInProgress, err = concurrency(root["concurrency"], staticWorkflowContext(event, workflowName, runID), referenceScope{})
			if err != nil {
				return nil, err
			}
			plan.Permissions, err = compilePermissions(fields["permissions"], permissions)
			if err == nil {
				err = permissionCeiling(plan.Permissions, scope.permissions)
			}
			if err != nil {
				return nil, err
			}
			if err = applyDefaults(&plan, fields["steps"], root["defaults"], fields["defaults"]); err != nil {
				return nil, err
			}
			scope.apply(&plan)
			plan.SecretNames = secretReferences(plan)
			plans = append(plans, plan)
			c.count++
			if c.count > 256 {
				return nil, diagnostic(root["jobs"], "expanded workflow exceeds 256 jobs")
			}
		}

	}
	if scope.gate == "" {
		group, cancel, e := concurrency(root["concurrency"], staticWorkflowContext(event, workflowName, runID), referenceScope{})
		if e != nil {
			return nil, e
		}
		for i := range plans {
			plans[i].WorkflowConcurrency = group
			plans[i].WorkflowCancelInProgress = cancel
		}
	}
	if len(parseErrors) > 0 {
		e := parseErrors[0]
		return nil, fmt.Errorf("line %d, column %d: workflow syntax: %s", e.Line, e.Column, e.Message)
	}
	return plans, nil
}

func compileJob(node *yaml.Node, key, workflowName string, workflowEnv map[string]string, needs []string, event run.Event, runID string, matrix map[string]any) (run.Plan, error) {
	fields, err := mapping(node, "name", "runs-on", "env", "steps", "timeout-minutes", "needs", "if", "outputs", "strategy", "continue-on-error", "concurrency", "permissions", "defaults")
	if err != nil {
		return run.Plan{}, err
	}
	early := referenceScope{matrix: true}
	ctx := staticContext(event, matrix)
	ctx.Values["github.workflow"] = workflowName
	ctx.Values["github.job"] = key
	ctx.Values["github.run_id"] = runID
	profile, err := staticString(fields["runs-on"], "", ctx, early)
	if err != nil || profile == "" {
		return run.Plan{}, diagnostic(fields["runs-on"], "runs-on requires a nonempty profile: %v", err)
	}
	name, err := staticString(fields["name"], key, ctx, early)
	if err != nil {
		return run.Plan{}, err
	}
	jobContinue, err := booleanSetting(fields["continue-on-error"], ctx, early)
	if err != nil {
		return run.Plan{}, err
	}
	group, cancelGroup, err := concurrency(fields["concurrency"], ctx, early)
	if err != nil {
		return run.Plan{}, err
	}

	if workflowName != "" {
		name = workflowName + " / " + name
	}
	scope := referenceScope{needs: make(map[string]bool, len(needs))}
	for _, need := range needs {
		scope.needs[need] = true
	}
	condition, err := condition(fields["if"], scope)
	if err != nil {
		return run.Plan{}, err
	}
	scope.matrix = true
	jobEnv, err := environment(fields["env"], scope)
	if err != nil {
		return run.Plan{}, err
	}
	mergedEnv := make(map[string]string, len(workflowEnv)+len(jobEnv))
	for k, value := range workflowEnv {
		mergedEnv[k] = value
	}
	for k, value := range jobEnv {
		mergedEnv[k] = value
	}
	timeout, err := duration(fields["timeout-minutes"], 1200)
	if err != nil {
		return run.Plan{}, err
	}
	stepsNode := fields["steps"]
	if stepsNode == nil || stepsNode.Kind != yaml.SequenceNode || len(stepsNode.Content) == 0 || len(stepsNode.Content) > 256 {
		return run.Plan{}, diagnostic(stepsNode, "steps must be a nonempty sequence of at most 256 steps")
	}
	steps := make([]run.Step, 0, len(stepsNode.Content))
	prior := make(map[string]bool)
	scope.steps, scope.env = prior, true
	allIDs := make(map[string]bool)
	for i, stepNode := range stepsNode.Content {
		step, explicitID, err := compileStep(stepNode, i, scope)
		if err != nil {
			return run.Plan{}, fmt.Errorf("step %d: %w", i+1, err)
		}
		if allIDs[step.ID] {
			return run.Plan{}, diagnostic(stepNode, "duplicate step ID %q", step.ID)
		}
		allIDs[step.ID] = true
		if explicitID {
			prior[step.ID] = true
		}
		steps = append(steps, step)
	}
	scope.env = false
	outputs, err := jobOutputs(fields["outputs"], scope)
	if err != nil {
		return run.Plan{}, err
	}
	sum := sha256.Sum256([]byte(runID + "\x00" + key))
	return run.Plan{
		WorkflowName: workflowName, Matrix: matrix, ContinueOnError: jobContinue, Concurrency: group, CancelInProgress: cancelGroup, EventName: event.Name, BaseRef: event.BaseRef, Inputs: event.Inputs, Vars: event.Vars,
		Key: key, Needs: needs, If: condition, Outputs: outputs,
		JobID: hex.EncodeToString(sum[:])[:24], RunID: runID, Name: name, Profile: profile,
		Repository: event.Repository, SHA: event.SHA, Ref: event.Ref, Event: append([]byte(nil), event.Payload...),
		Env: mergedEnv, Steps: steps, TimeoutSeconds: timeout,
	}, nil
}

func compileStep(node *yaml.Node, index int, scope referenceScope) (run.Step, bool, error) {
	fields, err := mapping(node, "id", "name", "run", "uses", "with", "env", "shell", "working-directory", "timeout-minutes", "if", "continue-on-error")
	if err != nil {
		return run.Step{}, false, err
	}
	id, err := optionalString(fields["id"], fmt.Sprintf("__step_%d", index+1))
	if err != nil {
		return run.Step{}, false, err
	}
	if !identifier.MatchString(id) || (fields["id"] != nil && strings.HasPrefix(id, "__step_")) {
		return run.Step{}, false, diagnostic(node, "invalid or reserved step ID %q", id)
	}
	name, err := optionalString(fields["name"], "")
	if err != nil {
		return run.Step{}, false, err
	}
	if strings.Contains(name, "${{") {
		return run.Step{}, false, diagnostic(fields["name"], "step name must be static")
	}
	condition, err := condition(fields["if"], scope)
	if err != nil {
		return run.Step{}, false, err
	}
	continueOnError := false
	if value := fields["continue-on-error"]; value != nil {
		if value.Tag == "!!str" && strings.HasPrefix(strings.TrimSpace(value.Value), "${{") {
			if err := expression.Validate(value.Value, scope.allowed); err != nil {
				return run.Step{}, false, diagnostic(value, "invalid continue-on-error: %s", err)
			}
		} else if value.Kind != yaml.ScalarNode || value.Tag != "!!bool" {
			return run.Step{}, false, diagnostic(value, "continue-on-error must be a literal boolean")
		}
		if value.Tag == "!!bool" {
			continueOnError, err = strconv.ParseBool(value.Value)
		}
		if err != nil {
			return run.Step{}, false, diagnostic(value, "continue-on-error must be a literal boolean")
		}
	}
	env, err := environment(fields["env"], scope)
	if err != nil {
		return run.Step{}, false, err
	}
	timeout, err := duration(fields["timeout-minutes"], 0)
	if err != nil {
		return run.Step{}, false, err
	}
	step := run.Step{ID: id, Name: name, Env: env, TimeoutSeconds: timeout, If: condition, ContinueOnError: continueOnError}
	if n := fields["continue-on-error"]; n != nil && n.Tag == "!!str" {
		step.ContinueOnErrorIf = n.Value
	}
	if (fields["run"] == nil) == (fields["uses"] == nil) {
		return run.Step{}, false, diagnostic(node, "step must contain exactly one of run or uses")
	}
	if fields["uses"] != nil {
		if fields["shell"] != nil || fields["working-directory"] != nil {
			return run.Step{}, false, diagnostic(node, "shell and working-directory are only supported for run steps")
		}
		uses, err := requiredString(fields["uses"])
		if err != nil {
			return run.Step{}, false, err
		}
		if uses != "actions/checkout@v4" || advancedCheckout(fields["with"]) {
			ref, err := action.Reference(uses)
			if err != nil {
				return run.Step{}, false, diagnostic(fields["uses"], "%s", err)
			}
			ref.Inputs = map[string]string{}
			if fields["with"] != nil {
				inputs, err := mapping(fields["with"])
				if err != nil {
					return run.Step{}, false, err
				}
				if len(inputs) > 100 {
					return run.Step{}, false, diagnostic(fields["with"], "at most 100 action inputs")
				}
				for key, n := range inputs {
					if !identifier.MatchString(key) {
						return run.Step{}, false, diagnostic(n, "invalid action input")
					}
					value, err := scalar(n)
					if err != nil {
						return run.Step{}, false, err
					}
					scope.secrets = true
					if err := validateExpressions(n, scope); err != nil {
						return run.Step{}, false, err
					}
					ref.Inputs[key] = value
				}
			}
			if action.BuiltinInputs(uses) != nil {
				if err := action.ValidateBuiltin(uses, ref.Inputs); err != nil {
					return run.Step{}, false, diagnostic(fields["with"], "%s", err)
				}
			}

			step.Action = ref
			return step, fields["id"] != nil, nil
		}
		checkout := &run.Checkout{FetchDepth: 1}
		if fields["with"] != nil {
			inputs, err := mapping(fields["with"], "path", "fetch-depth")
			if err != nil {
				return run.Step{}, false, err
			}
			checkout.Path, err = optionalString(inputs["path"], "")
			if err != nil {
				return run.Step{}, false, err
			}
			if !safeRelativePath(checkout.Path) || strings.Contains(checkout.Path, "${{") {
				return run.Step{}, false, diagnostic(inputs["path"], "checkout path must be a static path inside the workspace")
			}
			if inputs["fetch-depth"] != nil {
				value, err := scalar(inputs["fetch-depth"])
				if err != nil {
					return run.Step{}, false, err
				}
				checkout.FetchDepth, err = strconv.Atoi(value)
				if err != nil || checkout.FetchDepth < 0 {
					return run.Step{}, false, diagnostic(inputs["fetch-depth"], "fetch-depth must be a nonnegative integer")
				}
			}
		}
		step.Checkout = checkout
	} else {
		if fields["with"] != nil {
			return run.Step{}, false, diagnostic(fields["with"], "with is only supported for builtin actions")
		}
		step.Run, err = requiredString(fields["run"])
		if err != nil || strings.TrimSpace(step.Run) == "" {
			return run.Step{}, false, diagnostic(fields["run"], "run must be a nonempty string")
		}
		step.Shell, err = optionalString(fields["shell"], "bash")
		if err != nil {
			return run.Step{}, false, err
		}
		if step.Shell != "bash" && step.Shell != "sh" {
			return run.Step{}, false, diagnostic(fields["shell"], "supported shells are bash and sh")
		}
		step.WorkingDirectory, err = optionalString(fields["working-directory"], "")
		if err != nil {
			return run.Step{}, false, err
		}
		if !strings.Contains(step.WorkingDirectory, "${{") && !safeRelativePath(step.WorkingDirectory) {
			return run.Step{}, false, diagnostic(fields["working-directory"], "working-directory must stay inside the workspace")
		}
		for _, field := range []string{"run", "working-directory"} {
			if fields[field] != nil {
				scope.secrets = true
				if err := validateExpressions(fields[field], scope); err != nil {
					return run.Step{}, false, err
				}
			}
		}
	}
	return step, fields["id"] != nil, nil
}

func environment(node *yaml.Node, scope referenceScope) (map[string]string, error) {
	if node == nil {
		return nil, nil
	}
	scope.secrets = true
	fields, err := mapping(node)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(fields))
	for _, key := range sortedKeys(fields) {
		value := fields[key]
		if !envIdentifier.MatchString(key) || strings.HasPrefix(key, "GITHUB_") && !run.TokenEnvironment(key, value.Value) || strings.HasPrefix(key, "RUNNER_") || strings.HasPrefix(key, "FORGELET_") || key == "NODE_OPTIONS" {
			return nil, diagnostic(value, "invalid or reserved environment variable %q", key)
		}
		text, err := scalar(value)
		if err != nil {
			return nil, err
		}
		if err := validateExpressions(value, scope); err != nil {
			return nil, err
		}
		result[key] = text
	}
	// References from one key to another key in the same env mapping have no
	// deterministic evaluation order in Actions, so do not attempt recursive expansion.
	for _, key := range sortedKeys(fields) {
		for _, expression := range expressions(result[key]) {
			parts := strings.Split(expression, ".")
			if len(parts) == 2 && parts[0] == "env" {
				if _, exists := fields[parts[1]]; exists {
					return nil, diagnostic(fields[key], "environment values cannot reference variables in the same env mapping")
				}
			}
		}
	}
	return result, nil
}

func validateExpressions(node *yaml.Node, scope referenceScope) error {
	if err := expression.ValidateTemplate(node.Value, scope.allowed); err != nil {
		return diagnostic(node, "unsupported expression: %s", err)
	}
	return nil
}

func expressions(value string) []string {
	var result []string
	for {
		start := strings.Index(value, "${{")
		if start < 0 {
			return result
		}
		end := strings.Index(value[start+3:], "}}")
		if end < 0 {
			return result
		}
		result = append(result, strings.TrimSpace(value[start+3:start+3+end]))
		value = value[start+3+end+2:]
	}
}

func duration(node *yaml.Node, fallback int) (int, error) {
	if node == nil {
		return fallback, nil
	}
	value, err := scalar(node)
	if err != nil {
		return 0, err
	}
	minutes, err := strconv.Atoi(value)
	if err != nil || minutes <= 0 || minutes > 1440 {
		return 0, diagnostic(node, "timeout-minutes must be an integer from 1 to 1440")
	}
	return minutes * 60, nil
}

func mapping(node *yaml.Node, allowed ...string) (map[string]*yaml.Node, error) {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, diagnostic(node, "expected a mapping")
	}
	result := make(map[string]*yaml.Node, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			return nil, diagnostic(key, "mapping keys must be strings")
		}
		if _, exists := result[key.Value]; exists {
			return nil, diagnostic(key, "duplicate field %q", key.Value)
		}
		if len(allowed) > 0 {
			known := false
			for _, field := range allowed {
				known = known || field == key.Value
			}
			if !known {
				return nil, diagnostic(key, "unsupported field %q", key.Value)
			}
		}
		result[key.Value] = value
	}
	return result, nil
}

func scalar(node *yaml.Node) (string, error) {
	if node == nil || node.Kind != yaml.ScalarNode || (node.Tag != "!!str" && node.Tag != "!!int" && node.Tag != "!!bool" && node.Tag != "!!float") {
		return "", diagnostic(node, "expected a string, number, or boolean scalar")
	}
	if strings.ContainsRune(node.Value, 0) {
		return "", diagnostic(node, "NUL is not allowed")
	}
	return node.Value, nil
}

func requiredString(node *yaml.Node) (string, error) {
	if node == nil || node.Tag != "!!str" {
		return "", diagnostic(node, "expected a string")
	}
	return scalar(node)
}

func optionalString(node *yaml.Node, fallback string) (string, error) {
	if node == nil {
		return fallback, nil
	}
	return requiredString(node)
}

func validateTree(node *yaml.Node, depth int, count *int) error {
	*count++
	if depth > 32 || *count > 10000 {
		return diagnostic(node, "workflow is too complex")
	}
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return diagnostic(node, "YAML anchors and aliases are not supported")
	}
	for _, child := range node.Content {
		if err := validateTree(child, depth+1, count); err != nil {
			return err
		}
	}
	return nil
}

func safeRelativePath(value string) bool {
	clean := path.Clean(value)
	return !path.IsAbs(value) && clean != ".." && !strings.HasPrefix(clean, "../") && !strings.ContainsAny(value, "\\\x00")
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func diagnostic(node *yaml.Node, format string, args ...any) error {
	line, column := 1, 1
	if node != nil {
		line, column = node.Line, node.Column
	}
	return fmt.Errorf("line %d, column %d: %s", line, column, fmt.Sprintf(format, args...))
}

// Supply valid stand-in values for required inputs so validation checks their
// definitions without requiring a particular manual dispatch payload.
func validateDispatchDefinition(on *yaml.Node) error {
	if on == nil || on.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(on.Content); i += 2 {
		if on.Content[i].Value != "workflow_dispatch" {
			continue
		}
		if on.Content[i+1].Tag == "!!null" {
			return nil
		}
		fields, err := mapping(on.Content[i+1])
		if err != nil {
			return err
		}
		values := map[string]any{}
		if defs := fields["inputs"]; defs != nil {
			inputs, err := mapping(defs)
			if err != nil {
				return err
			}
			for name, node := range inputs {
				f, err := mapping(node)
				if err != nil {
					return err
				}
				if f["default"] != nil {
					continue
				}
				kind := "string"
				if f["type"] != nil {
					kind = f["type"].Value
				}
				switch kind {
				case "boolean":
					values[name] = false
				case "number":
					values[name] = float64(0)
				case "choice":
					if options := f["options"]; options != nil && len(options.Content) > 0 {
						values[name] = options.Content[0].Value
					}
				default:
					values[name] = "validate"
				}
			}
		}
		_, err = compileTrigger(on, run.Event{Name: "workflow_dispatch", Inputs: values})
		return err
	}
	return nil
}

func workflowDocument(source []byte) (*yaml.Node, error) {
	if len(source) > maxSourceBytes {
		return nil, fmt.Errorf("workflow exceeds %d bytes", maxSourceBytes)
	}
	dec := yaml.NewDecoder(bytes.NewReader(source))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode workflow: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, fmt.Errorf("decode workflow: %w", err)
		}
		return nil, fmt.Errorf("workflow must contain exactly one YAML document")
	}
	count := 0
	if err := validateTree(&doc, 0, &count); err != nil {
		return nil, err
	}
	if len(doc.Content) != 1 {
		return nil, diagnostic(&doc, "workflow must be a mapping")
	}

	return &doc, nil
}

// The original fixed checkout remains available to native profiles. Full checkout
// inputs are retained for the generic Actions backend and diagnosed at admission.
func advancedCheckout(node *yaml.Node) bool {
	if node == nil {
		return false
	}
	fields, err := mapping(node)
	if err != nil {
		return true
	}
	for name := range fields {
		if name != "path" && name != "fetch-depth" {
			return true
		}
	}
	return false
}
