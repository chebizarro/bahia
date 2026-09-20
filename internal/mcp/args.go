package mcp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

func stringArg(args map[string]interface{}, name string) string {
	if v, ok := args[name].(string); ok {
		return v
	}
	return ""
}

func boolArg(args map[string]interface{}, name string) bool {
	if v, ok := args[name].(bool); ok {
		return v
	}
	return false
}

func int64Arg(args map[string]interface{}, name string) int64 {
	switch v := args[name].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case json.Number:
		n, _ := v.Int64()
		return n
	default:
		return 0
	}
}

func anyMapFromArg(raw interface{}) map[string]any {
	if raw == nil {
		return nil
	}
	if value, ok := raw.(map[string]any); ok {
		return value
	}
	return nil
}

func stringMapFromArg(raw interface{}) map[string]string {
	if raw == nil {
		return nil
	}
	out := map[string]string{}
	switch value := raw.(type) {
	case map[string]string:
		for k, v := range value {
			out[k] = v
		}
	case map[string]any:
		for k, v := range value {
			out[k] = fmt.Sprint(v)
		}
	default:
		return nil
	}
	return out
}

func stringSliceFromArg(raw interface{}) []string {
	switch value := raw.(type) {
	case []string:
		return value
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if s := strings.TrimSpace(fmt.Sprint(item)); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func optionalIntArg(args map[string]interface{}, name string, defaultValue int) int {
	switch v := args[name].(type) {
	case float64:
		return int(v)
	case float32:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case json.Number:
		if parsed, err := v.Int64(); err == nil {
			return int(parsed)
		}
	}
	return defaultValue
}

func optionalUUIDArg(args map[string]interface{}, name string) uuid.UUID {
	value := stringArg(args, name)
	if value == "" {
		return uuid.Nil
	}
	id, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil
	}
	return id
}

func optionalUUIDArgStrict(args map[string]interface{}, name string) (uuid.UUID, error) {
	value := strings.TrimSpace(stringArg(args, name))
	if value == "" {
		return uuid.Nil, nil
	}
	id, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid %s: %w", name, err)
	}
	return id, nil
}

func parseUUIDArg(args map[string]interface{}, key string) (uuid.UUID, error) {
	raw, _ := args[key].(string)
	if raw == "" {
		return uuid.Nil, fmt.Errorf("%s is required", key)
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid %s: %w", key, err)
	}
	return id, nil
}

func parseRequiredUUIDArg(args map[string]interface{}, name string) (uuid.UUID, error) {
	value, _ := args[name].(string)
	if value == "" {
		return uuid.Nil, fmt.Errorf("%s is required", name)
	}
	id, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid %s: %w", name, err)
	}
	return id, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}