package repository

import (
	"encoding/json"
	"fmt"
)

// marshalJSON marshals v to JSON, returning a descriptive error on failure.
// Returns []byte("null") for nil input to match PostgreSQL NULL semantics.
func marshalJSON(v any, fieldName string) ([]byte, error) {
	if v == nil {
		return []byte("null"), nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshaling %s: %w", fieldName, err)
	}
	return b, nil
}

// unmarshalJSON safely unmarshals data into v.
// Returns nil for nil or empty data (handles PostgreSQL NULL columns).
// Returns a descriptive error if the data is present but not valid JSON.
func unmarshalJSON(data []byte, v any, fieldName string) error {
	if len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("unmarshaling %s: %w", fieldName, err)
	}
	return nil
}
