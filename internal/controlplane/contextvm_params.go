package controlplane

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
)

func decodeStrictContextVMParams(params json.RawMessage, out any) error {
	if len(params) == 0 || string(params) == "null" {
		params = []byte(`{}`)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(params, &envelope); err != nil {
		return fmt.Errorf("invalid environment params: %w", err)
	}
	delete(envelope, "_meta")
	businessParams, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("invalid environment params: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(businessParams))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("invalid environment params: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("invalid environment params: multiple JSON values")
		}
		return fmt.Errorf("invalid environment params: %w", err)
	}
	return nil
}

func effectiveIdempotencyKey(request ContextVMRequest, compatibilityKey string) string {
	if token := strings.TrimSpace(request.ProgressToken); token != "" {
		return token
	}
	return strings.TrimSpace(compatibilityKey)
}

func supportsManagedRuntimeConfig(runtimeType domain.RuntimeType) bool {
	switch runtimeType {
	case domain.RuntimeTypeDocker, domain.RuntimeTypeCompose:
		return true
	default:
		return false
	}
}
