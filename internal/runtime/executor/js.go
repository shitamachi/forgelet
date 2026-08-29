package executor

import (
	"context"
	"errors"
	"fmt"

	"github.com/dop251/goja"

	"github.com/shitamachi/forgelet/internal/run/plan"
)

// runJS executes a JS action via goja. For actions/github-script, script is
// the with.script input; for other JS actions, it would be the fetched main.
func runJS(ctx context.Context, bc BuiltinContext, script string, js *plan.JSStep) error {
	// Prefer the explicit script arg (which is with.script for github-script)
	if script == "" && js != nil && js.Script != "" {
		script = js.Script
	}
	if script == "" {
		script = bc.Inputs["script"]
	}
	if script == "" {
		return fmt.Errorf("js: no script to execute")
	}
	// For non-github-script JS actions, we would need to fetch the main file.
	// For V1 we only support actions/github-script; other JS actions are
	// treated as not yet supported.
	if js != nil && js.Repo != "actions/github-script" && js.Repo != "" {
		return fmt.Errorf("js: action %s not yet supported (only actions/github-script)", js.Repo)
	}
	vm := goja.New()
	// Setup core, github, exec
	core := map[string]interface{}{
		"getInput": func(call goja.FunctionCall) goja.Value {
			key := call.Argument(0).String()
			if v, ok := bc.Inputs[key]; ok {
				return vm.ToValue(v)
			}
			return vm.ToValue("")
		},
		"setOutput": func(call goja.FunctionCall) goja.Value {
			k := call.Argument(0).String()
			v := call.Argument(1).String()
			if bc.SetOutput != nil {
				bc.SetOutput(k, v)
			}
			return goja.Undefined()
		},
		"setFailed": func(call goja.FunctionCall) goja.Value {
			msg := call.Argument(0).String()
			bc.Logger.Error("js setFailed", "msg", msg)
			// We can't directly fail the step from here, but we can panic with a special error
			// that the handler will catch and return as failure.
			panic(fmt.Errorf("js setFailed: %s", msg))
		},
		"addPath": func(call goja.FunctionCall) goja.Value {
			p := call.Argument(0).String()
			bc.Logger.Info("js addPath", "path", p)
			return goja.Undefined()
		},
		"exportVariable": func(call goja.FunctionCall) goja.Value {
			k := call.Argument(0).String()
			v := call.Argument(1).String()
			bc.Logger.Info("js exportVariable", "key", k, "value", v)
			return goja.Undefined()
		},
	}
	github := map[string]interface{}{
		"context": map[string]interface{}{
			"eventName": "push",
			"sha":       "abc",
			"ref":       "refs/heads/main",
			"actor":     "test",
			"payload":   map[string]interface{}{},
		},
	}
	_ = vm.Set("core", core)
	_ = vm.Set("github", github)
	_ = vm.Set("console", map[string]interface{}{
		"log": func(call goja.FunctionCall) goja.Value {
			bc.Logger.Info("js console.log", "args", fmt.Sprint(call.Arguments))
			return goja.Undefined()
		},
	})
	// Handle context cancellation
	go func() {
		<-ctx.Done()
		vm.Interrupt("context cancelled")
	}()
	_, err := vm.RunString(script)
	if err != nil {
		var target *goja.Exception
		if errors.As(err, &target) {
			return fmt.Errorf("js: %v", target.Value())
		}
		return fmt.Errorf("js: %w", err)
	}
	bc.Logger.Info("js executed", "script", script[:min(100, len(script))])
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
