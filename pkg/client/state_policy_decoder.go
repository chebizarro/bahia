package client

import (
	"encoding/json"
	"fmt"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// DecodeState decodes the service-state 30900 record published by RuntimeStateRecord.
func DecodeState(ev nostr.Event) (*domain.EnvironmentServiceState, error) {
	decoded, err := DecodeControlStateEvent(ev)
	if err != nil {
		return nil, err
	}
	if decoded.LegacyKind != kinds.ServiceState || decoded.Topic != kinds.CPStateTopicServiceState {
		return nil, fmt.Errorf("event is not a service-state record")
	}
	if decoded.Deleted {
		return nil, nil
	}
	// Earlier service-state publishers encoded an absent unit as "". The
	// domain UUID decoder requires null or an omitted field, so normalize
	// that historical wire value while the relay replaces older records.
	content := decoded.Content
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(content, &raw); err != nil {
		return nil, fmt.Errorf("decode service-state content: %w", err)
	}
	if string(raw["deployment_unit_id"]) == `""` {
		raw["deployment_unit_id"] = json.RawMessage("null")
		content, err = json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("normalize service-state content: %w", err)
		}
	}
	var state domain.EnvironmentServiceState
	if err := json.Unmarshal(content, &state); err != nil {
		return nil, fmt.Errorf("decode service-state content: %w", err)
	}
	if state.ServiceID == uuid.Nil || state.EnvironmentID == uuid.Nil {
		return nil, fmt.Errorf("service-state record is missing service_id or environment_id")
	}
	return &state, nil
}

// DecodePolicy decodes the policy-registry 30900 record published by PolicyRegistryRecord.
func DecodePolicy(ev nostr.Event) (*domain.DeploymentPolicy, error) {
	decoded, err := DecodeControlStateEvent(ev)
	if err != nil {
		return nil, err
	}
	if decoded.LegacyKind != kinds.PolicyRegistry || decoded.Topic != kinds.CPStateTopicPolicyRegistry {
		return nil, fmt.Errorf("event is not a policy-registry record")
	}
	if decoded.Deleted {
		return nil, nil
	}
	var policy domain.DeploymentPolicy
	if err := json.Unmarshal(decoded.Content, &policy); err != nil {
		return nil, fmt.Errorf("decode policy-registry content: %w", err)
	}
	if policy.ID == uuid.Nil {
		return nil, fmt.Errorf("policy-registry record is missing id")
	}
	return &policy, nil
}
