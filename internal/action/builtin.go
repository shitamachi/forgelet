package action

import "fmt"

// BuiltinInputs is the complete supported input surface, shared by the compiler
// and executor. These adapters use forgelet storage, not GitHub's runner API.
func BuiltinInputs(uses string) map[string]string {
	switch uses {
	case "actions/upload-artifact@v4":
		return map[string]string{"name": "artifact", "path": "", "retention-days": "7", "if-no-files-found": "warn", "include-hidden-files": "false"}
	case "actions/download-artifact@v4":
		return map[string]string{"name": "artifact", "path": "."}
	case "actions/cache@v4", "actions/cache/restore@v4":
		return map[string]string{"path": "", "key": "", "restore-keys": "", "fail-on-cache-miss": "false", "lookup-only": "false"}
	case "actions/cache/save@v4":
		return map[string]string{"path": "", "key": ""}
	case "forgelet/build@v1":
		return map[string]string{"context": ".", "file": "Dockerfile", "image": "", "target": "", "build-args": ""}
	case "forgelet/deploy@v1":
		return map[string]string{"target": "", "digest": ""}
	}
	return nil
}

func ValidateBuiltin(uses string, inputs map[string]string) error {
	allowed := BuiltinInputs(uses)
	for key := range inputs {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("unsupported %s input %q", uses, key)
		}
	}
	for _, key := range []string{"path", "key", "image", "target", "digest"} {
		// target is optional for Docker multi-stage selection.
		if key == "target" && uses == "forgelet/build@v1" {
			continue
		}
		if fallback, ok := allowed[key]; ok && fallback == "" && inputs[key] == "" {
			return fmt.Errorf("%s requires input %s", uses, key)
		}
	}
	return nil
}
