package run

import "fmt"

// ValidateCallInput never coerces a caller's string into boolean or number.
func ValidateCallInput(kind string, v any) error {
	valid := false
	switch kind {
	case "string":
		_, valid = v.(string)
	case "boolean":
		_, valid = v.(bool)
	case "number":
		switch v.(type) {
		case float64, int, int64:
			valid = true
		}
	}
	if !valid {
		return fmt.Errorf("workflow input requires %s", kind)
	}
	if text, ok := v.(string); ok && len(text) > 8192 {
		return fmt.Errorf("workflow input exceeds 8 KiB")
	}
	return nil
}
