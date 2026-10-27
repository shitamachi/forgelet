package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v4"

	"github.com/shitamachi/forgelet/internal/expression"
	"github.com/shitamachi/forgelet/internal/run"
)

type bundleCompiler struct {
	files  map[string][]byte
	active map[string]bool
	count  int
}
type callScope struct {
	deferred                                      map[string]bool
	prefix, gate, workflowName, source, eventName string
	permissions                                   map[string]string
	bindings                                      map[string]string
	restricted                                    bool
	required                                      []string
}

// CompileFiles compiles a caller and its same-repository reusable workflows from
// one immutable source snapshot. It performs no source fetching or secret reads.
func CompileFiles(source []byte, event run.Event, runID string, files map[string][]byte) ([]run.Plan, error) {
	return compileFiles(source, event, runID, files, false)
}
func ValidateFiles(source []byte, event run.Event, files map[string][]byte) ([]run.Plan, error) {
	return compileFiles(source, event, "validation", files, true)
}
func compileFiles(source []byte, event run.Event, id string, files map[string][]byte, validating bool) ([]run.Plan, error) {
	if len(files) > 100 {
		return nil, fmt.Errorf("workflow bundle exceeds 100 files")
	}
	total := 0
	for name, data := range files {
		if !workflowPath(name) {
			return nil, fmt.Errorf("invalid workflow bundle path %q", name)
		}
		total += len(data)
	}
	if total > 4<<20 {
		return nil, fmt.Errorf("workflow bundle exceeds 4 MiB")
	}
	if event.Name == "" {
		event.Name = "push"
	}
	c := bundleCompiler{files: files, active: map[string]bool{event.WorkflowPath: true}}
	return c.compile(source, event, id, validating, callScope{source: event.WorkflowPath, eventName: event.Name})
}
func workflowPath(name string) bool {
	return strings.HasPrefix(name, ".github/workflows/") && !strings.Contains(strings.TrimPrefix(name, ".github/workflows/"), "/") && (strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml")) && !strings.Contains(name, "..")
}
func (s callScope) apply(p *run.Plan) {
	p.Needs = append([]string(nil), p.Needs...)
	p.NeedsAliases = map[string]string{}
	for i, key := range p.Needs {
		p.Needs[i] = s.prefix + key
		p.NeedsAliases[p.Needs[i]] = key
	}
	p.Key = s.prefix + p.Key
	p.Group = s.prefix + p.Group
	if s.gate != "" {
		p.Needs = append(p.Needs, s.gate)
	}
	p.ParentGate = s.gate
	p.SecretBindings = s.bindings
	p.RestrictSecrets = s.restricted
	p.RequiredSecrets = s.required
	p.SourceWorkflow = s.source
	if s.eventName != "" {
		p.EventName = s.eventName
	}
	sum := sha256.Sum256([]byte(p.RunID + "\x00" + p.Key))
	p.JobID = hex.EncodeToString(sum[:])[:24]
}
func decodeRoot(source []byte) (map[string]*yaml.Node, error) {
	n, err := workflowDocument(source)
	if err != nil {
		return nil, err
	}
	return mapping(n.Content[0])
}
func (c *bundleCompiler) compileCall(node *yaml.Node, key, name string, needs []string, event run.Event, id string, validating bool, parent callScope, permissions map[string]string) ([]run.Plan, error) {
	fields, err := mapping(node, "name", "uses", "needs", "if", "with", "secrets", "permissions", "concurrency", "strategy")
	if err != nil {
		return nil, err
	}
	if fields["concurrency"] != nil {
		return nil, diagnostic(fields["concurrency"], "execution capability concurrency spanning workflow calls is not implemented")
	}
	if fields["strategy"] != nil {
		return nil, diagnostic(fields["strategy"], "execution capability matrix on workflow calls is not implemented")
	}
	uses, err := requiredString(fields["uses"])
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(uses, "./") || !workflowPath(strings.TrimPrefix(uses, "./")) {
		return nil, diagnostic(fields["uses"], "reusable workflow requires a same-repository ./.github/workflows/file.yml reference; remote calls are not implemented")
	}
	sourcePath := strings.TrimPrefix(uses, "./")
	source, exists := c.files[sourcePath]
	if !exists {
		return nil, fmt.Errorf("reusable workflow %s is missing from the source snapshot", sourcePath)
	}
	if c.active[sourcePath] {
		return nil, fmt.Errorf("reusable workflow cycle at %s", sourcePath)
	}
	if len(c.active) >= 8 {
		return nil, fmt.Errorf("reusable workflow nesting exceeds 8")
	}
	root, err := decodeRoot(source)
	if err != nil {
		return nil, err
	}
	onNode := root["on"]
	var on map[string]*yaml.Node
	hasCall := false
	if onNode != nil {
		switch onNode.Kind {
		case yaml.ScalarNode:
			hasCall = onNode.Value == "workflow_call"
		case yaml.SequenceNode:
			for _, n := range onNode.Content {
				hasCall = hasCall || n.Value == "workflow_call"
			}
		}
	}
	if hasCall {
		on = map[string]*yaml.Node{"workflow_call": {Tag: "!!null"}}
	} else {
		on, err = mapping(onNode)
	}
	if err != nil {
		return nil, fmt.Errorf("called workflow must declare on.workflow_call: %w", err)
	}
	declaration, ok := on["workflow_call"]
	if !ok {
		return nil, fmt.Errorf("called workflow must declare on.workflow_call")
	}
	var definition map[string]*yaml.Node
	if declaration.Tag != "!!null" {
		definition, err = mapping(declaration, "inputs", "secrets", "outputs")
		if err != nil {
			return nil, err
		}
	}
	specs, placeholder, err := callInputs(definition["inputs"], fields["with"], needs, event, parent.deferred)
	if err != nil {
		return nil, err
	}
	// A call gate reuses ordinary job condition/concurrency validation, without an executor.
	gateNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for i := 0; i < len(node.Content); i += 2 {
		switch node.Content[i].Value {
		case "name", "needs", "if", "concurrency":
			gateNode.Content = append(gateNode.Content, node.Content[i], node.Content[i+1])
		}
	}
	var dummy yaml.Node
	_ = yaml.Unmarshal([]byte("runs-on: control\nsteps:\n  - run: 'true'\n"), &dummy)
	gateNode.Content = append(gateNode.Content, dummy.Content[0].Content...)
	gate, err := compileJob(gateNode, key, name, nil, needs, event, id, nil)
	if err != nil {
		return nil, err
	}
	gate.Group = key
	parent.apply(&gate)
	resultKey := gate.Key
	gate.Key = "$start/" + resultKey
	gate.Group = gate.Key
	sum := sha256.Sum256([]byte(id + "\x00" + gate.Key))
	gate.JobID = hex.EncodeToString(sum[:])[:24]
	gate.Control = "call_gate"
	gate.Profile = ""
	gate.Steps = []run.Step{}
	gate.CallInputs = specs
	child := callScope{prefix: resultKey + "/", gate: gate.Key, workflowName: name, source: sourcePath, eventName: parent.eventName}
	child.deferred = map[string]bool{}
	for input, spec := range specs {
		if text, ok := spec.Value.(string); ok && strings.Contains(text, "${{") {
			_ = expression.ValidateTemplate(text, func(ref string) bool {
				if runtimeCallReference(ref, parent.deferred) {
					child.deferred[input] = true
				}
				return true
			})
		}
	}
	child.permissions, err = compilePermissions(fields["permissions"], permissions)
	if err == nil {
		err = permissionCeiling(child.permissions, parent.permissions)
	}
	if err != nil {
		return nil, err
	}
	child.restricted = true
	child.bindings = map[string]string{}
	if secretNode := fields["secrets"]; secretNode != nil {
		if secretNode.Kind == yaml.ScalarNode && secretNode.Value == "inherit" {
			child.restricted = parent.restricted
			child.bindings = parent.bindings
		} else {
			bindings, e := mapping(secretNode)
			if e != nil {
				return nil, e
			}
			for local, n := range bindings {
				if !identifier.MatchString(local) {
					return nil, diagnostic(n, "invalid secret name")
				}
				text, e := requiredString(n)
				if e != nil {
					return nil, e
				}
				refs := []string{}
				e = expression.ValidateTemplate(text, func(ref string) bool {
					refs = append(refs, ref)
					return strings.HasPrefix(ref, "secrets.") && identifier.MatchString(strings.TrimPrefix(ref, "secrets."))
				})
				if e != nil || len(refs) != 1 || strings.TrimSpace(text) != "${{ "+refs[0]+" }}" && strings.Join(strings.Fields(text), "") != "${{"+refs[0]+"}}" {
					return nil, diagnostic(n, "secret mapping requires a direct secrets.NAME expression")
				}
				original := strings.TrimPrefix(refs[0], "secrets.")
				if parent.restricted {
					original = parent.bindings[original]
				}
				child.bindings[local] = original
			}
		}
	}
	declaredSecrets, err := mappingOrEmpty(definition["secrets"])
	if err != nil {
		return nil, err
	}
	if fields["secrets"] != nil && fields["secrets"].Kind == yaml.MappingNode {
		for local := range child.bindings {
			if declaredSecrets[local] == nil {
				return nil, fmt.Errorf("undeclared workflow secret %q", local)
			}
		}
	}
	for secret, n := range declaredSecrets {
		spec, e := mapping(n, "description", "required")
		if e != nil {
			return nil, e
		}
		if req := spec["required"]; req != nil && req.Value == "true" {
			original := secret
			if child.restricted {
				original = child.bindings[secret]
			}
			if original == "" {
				return nil, fmt.Errorf("required workflow secret %q is not passed", secret)
			}
			child.required = append(child.required, original)
		}
	}
	gate.RequiredSecrets = append(gate.RequiredSecrets, child.required...)
	childEvent := event
	childEvent.Name = "workflow_call"
	childEvent.Inputs = placeholder
	c.active[sourcePath] = true
	children, err := c.compile(source, childEvent, id, validating, child)
	delete(c.active, sourcePath)
	if err != nil {
		return nil, fmt.Errorf("call %s: %w", sourcePath, err)
	}
	result := gate
	result.Control = "call_result"
	result.Key = resultKey
	result.Group = resultKey
	result.ParentGate = gate.Key
	result.If = "always()"
	result.CallInputs = nil
	result.Needs = []string{gate.Key}
	result.NeedsAliases = map[string]string{}
	result.Concurrency = ""
	result.CancelInProgress = false
	result.Inputs = placeholder
	sum = sha256.Sum256([]byte(id + "\x00" + resultKey))
	result.JobID = hex.EncodeToString(sum[:])[:24]
	childJobs, err := mapping(root["jobs"])
	if err != nil {
		return nil, err
	}
	for _, local := range sortedKeys(childJobs) {
		global := child.prefix + local
		result.Needs = append(result.Needs, global)
		result.NeedsAliases[global] = local
	}
	result.Outputs = map[string]string{}
	outputs, err := mappingOrEmpty(definition["outputs"])
	if err != nil {
		return nil, err
	}
	if len(outputs) > 100 {
		return nil, fmt.Errorf("at most 100 workflow outputs")
	}
	for output, n := range outputs {
		if !identifier.MatchString(output) {
			return nil, diagnostic(n, "invalid workflow output name")
		}
		spec, e := mapping(n, "description", "value")
		if e != nil {
			return nil, e
		}
		value, e := requiredString(spec["value"])
		if e != nil {
			return nil, e
		}
		if e = expression.ValidateTemplate(value, func(ref string) bool {
			parts := strings.Split(ref, ".")
			return len(parts) == 4 && parts[0] == "jobs" && childJobs[parts[1]] != nil && parts[2] == "outputs" && identifier.MatchString(parts[3])
		}); e != nil {
			return nil, fmt.Errorf("workflow output %s: %w", output, e)
		}
		result.Outputs[output] = value
	}
	c.count += 2
	if c.count > 256 {
		return nil, fmt.Errorf("expanded workflow exceeds 256 jobs")
	}
	return append(append([]run.Plan{gate}, children...), result), nil
}
func mappingOrEmpty(n *yaml.Node) (map[string]*yaml.Node, error) {
	if n == nil {
		return map[string]*yaml.Node{}, nil
	}
	return mapping(n)
}
func callInputs(definition, with *yaml.Node, needs []string, event run.Event, deferredInputs map[string]bool) (map[string]run.CallInput, map[string]any, error) {
	definitions, err := mappingOrEmpty(definition)
	if err != nil {
		return nil, nil, err
	}
	provided, err := mappingOrEmpty(with)
	if err != nil {
		return nil, nil, err
	}
	if len(definitions) > 25 {
		return nil, nil, fmt.Errorf("at most 25 workflow inputs")
	}
	for name := range provided {
		if definitions[name] == nil {
			return nil, nil, fmt.Errorf("undeclared workflow input %q", name)
		}
	}
	specs := map[string]run.CallInput{}
	placeholder := map[string]any{}
	scope := referenceScope{needs: map[string]bool{}}
	for _, need := range needs {
		scope.needs[need] = true
	}
	for name, n := range definitions {
		if !identifier.MatchString(name) {
			return nil, nil, diagnostic(n, "invalid workflow input name")
		}
		fields, e := mapping(n, "description", "type", "required", "default")
		if e != nil {
			return nil, nil, e
		}
		kind, e := requiredString(fields["type"])
		if e != nil {
			return nil, nil, e
		}
		var zero any
		switch kind {
		case "string":
			zero = ""
		case "boolean":
			zero = false
		case "number":
			zero = float64(0)
		default:
			return nil, nil, diagnostic(fields["type"], "workflow input type must be string, boolean or number")
		}
		value := provided[name]
		if value == nil {
			value = fields["default"]
		}
		if value == nil && fields["required"] != nil && fields["required"].Value == "true" {
			return nil, nil, fmt.Errorf("required workflow input %q is missing", name)
		}
		input := run.CallInput{Type: kind, Value: zero}
		placeholder[name] = zero
		if value != nil {
			var v any
			if e = value.Decode(&v); e != nil {
				return nil, nil, e
			}
			if text, ok := v.(string); ok && strings.Contains(text, "${{") {
				deferred := false
				if e = expression.ValidateTemplate(text, func(ref string) bool {
					deferred = deferred || runtimeCallReference(ref, deferredInputs)
					return scope.allowed(ref)
				}); e != nil {
					return nil, nil, e
				}
				count := 0
				layout, _ := expression.Parts(text, func(string) (string, error) { count++; return "EXPRESSION", nil })
				whole := count == 1 && strings.TrimSpace(layout) == "EXPRESSION"
				if !whole {
					if kind != "string" {
						return nil, nil, fmt.Errorf("typed workflow input expression must occupy the entire value")
					}
					input.Template = true
				}
				input.Value = text
				// Static contexts can be resolved now for names/profiles; needs values wait for the gate.
				if !deferred {
					ctx := staticContext(event, nil)
					var resolved any
					var e error
					if input.Template {
						resolved, e = expression.Interpolate(text, ctx)
					} else {
						resolved, e = expression.Eval(text, ctx)
					}
					if e != nil {
						return nil, nil, e
					}
					if e = run.ValidateCallInput(kind, resolved); e != nil {
						return nil, nil, fmt.Errorf("input %s: %w", name, e)
					}
					placeholder[name] = resolved
				}
			} else {
				if e = run.ValidateCallInput(kind, v); e != nil {
					return nil, nil, fmt.Errorf("input %s: %w", name, e)
				}
				input.Value = v
				placeholder[name] = v
			}
		}
		specs[name] = input
	}
	return specs, placeholder, nil
}

// HasCalls is a source-loading hint, not an admission check. CompileFiles remains
// responsible for validating the complete syntax and execution capabilities.
func HasCalls(source []byte) bool {
	root, err := decodeRoot(source)
	if err != nil {
		return false
	}
	jobs, err := mapping(root["jobs"])
	if err != nil {
		return false
	}
	for _, node := range jobs {
		fields, err := mapping(node)
		if err == nil && fields["uses"] != nil {
			return true
		}
	}
	return false
}

// runtimeCallReference classifies AST references, including whole-object and
// dynamic-index access, without guessing from expression source text.
func runtimeCallReference(ref string, inputs map[string]bool) bool {
	parts := strings.Split(ref, ".")
	if parts[0] == "needs" {
		return true
	}
	if parts[0] != "inputs" {
		return false
	}
	if len(parts) == 1 || parts[1] == "*" {
		return len(inputs) > 0
	}
	return inputs[parts[1]]
}
