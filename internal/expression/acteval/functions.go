// Derived from nektos/act v0.2.89 (MIT); see NOTICE and LICENSE.
package acteval

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/rhysd/actionlint"
)

func (impl *Interpreter) contains(search, item reflect.Value) (bool, error) {
	switch search.Kind() {
	case reflect.String, reflect.Int, reflect.Float64, reflect.Bool, reflect.Invalid:
		return strings.Contains(
			strings.ToLower(impl.coerceToString(search).String()),
			strings.ToLower(impl.coerceToString(item).String()),
		), nil

	case reflect.Slice:
		for i := 0; i < search.Len(); i++ {
			arrayItem := search.Index(i).Elem()
			result, err := impl.compareValues(arrayItem, item, actionlint.CompareOpNodeKindEq)
			if err != nil {
				return false, err
			}

			if isEqual, ok := result.(bool); ok && isEqual {
				return true, nil
			}
		}
	}

	return false, nil
}

func (impl *Interpreter) startsWith(searchString, searchValue reflect.Value) (bool, error) {
	return strings.HasPrefix(
		strings.ToLower(impl.coerceToString(searchString).String()),
		strings.ToLower(impl.coerceToString(searchValue).String()),
	), nil
}

func (impl *Interpreter) endsWith(searchString, searchValue reflect.Value) (bool, error) {
	return strings.HasSuffix(
		strings.ToLower(impl.coerceToString(searchString).String()),
		strings.ToLower(impl.coerceToString(searchValue).String()),
	), nil
}

// format bounds every write, including substitutions, before allocation.
func (impl *Interpreter) format(str reflect.Value, values ...reflect.Value) (string, error) {
	input := impl.coerceToString(str).String()
	if len(input) > 64<<10 {
		return "", fmt.Errorf("format input exceeds 64 KiB")
	}
	var out strings.Builder
	write := func(value string) error {
		if out.Len()+len(value) > 64<<10 {
			return fmt.Errorf("format result exceeds 64 KiB")
		}
		out.WriteString(value)
		return nil
	}
	for i := 0; i < len(input); {
		switch input[i] {
		case '{':
			if i+1 < len(input) && input[i+1] == '{' {
				if err := write("{"); err != nil {
					return "", err
				}
				i += 2
				continue
			}
			end := i + 1
			for end < len(input) && input[end] >= '0' && input[end] <= '9' {
				end++
			}
			if end == i+1 || end >= len(input) || input[end] != '}' {
				return "", fmt.Errorf("invalid format placeholder")
			}
			index, err := strconv.ParseUint(input[i+1:end], 10, 32)
			if err != nil || index >= uint64(len(values)) {
				return "", fmt.Errorf("format argument is missing")
			}
			if err = write(impl.coerceToString(values[index]).String()); err != nil {
				return "", err
			}
			i = end + 1
		case '}':
			if i+1 >= len(input) || input[i+1] != '}' {
				return "", fmt.Errorf("unmatched format closing bracket")
			}
			if err := write("}"); err != nil {
				return "", err
			}
			i += 2
		default:
			end := i + 1
			for end < len(input) && input[end] != '{' && input[end] != '}' {
				end++
			}
			if err := write(input[i:end]); err != nil {
				return "", err
			}
			i = end
		}
	}
	return out.String(), nil
}

func (impl *Interpreter) join(array reflect.Value, sep reflect.Value) (string, error) {
	separator := impl.coerceToString(sep).String()
	switch array.Kind() {
	case reflect.Slice:
		var items []string
		for i := 0; i < array.Len(); i++ {
			items = append(items, impl.coerceToString(array.Index(i).Elem()).String())
		}

		return strings.Join(items, separator), nil
	default:
		return strings.Join([]string{impl.coerceToString(array).String()}, separator), nil
	}
}

func (impl *Interpreter) toJSON(value reflect.Value) (string, error) {
	if value.Kind() == reflect.Invalid {
		return "null", nil
	}

	json, err := json.MarshalIndent(value.Interface(), "", "  ")
	if err != nil {
		return "", fmt.Errorf("convert value to JSON: %w", err)
	}

	return string(json), nil
}

func (impl *Interpreter) fromJSON(value reflect.Value) (interface{}, error) {
	if value.Kind() != reflect.String {
		return nil, fmt.Errorf("cannot parse non-string type %v as JSON", value.Kind())
	}

	var data interface{}

	err := json.Unmarshal([]byte(value.String()), &data)
	if err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	return data, nil
}
