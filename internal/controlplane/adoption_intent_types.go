package controlplane

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

type adoptionScanEventRequest struct {
	Targets []adoptionEventTarget `json:"targets"`
	Offset  int                   `json:"offset,omitempty"`
	Limit   int                   `json:"limit,omitempty"`
}

func validateAdoptionSecretRefPayload(content map[string]interface{}) error {
	if _, ok := content["value"]; ok {
		return fmt.Errorf("adoption intents never carry secret values")
	}
	if _, ok := content["encrypted_value"]; ok {
		return fmt.Errorf("adoption intents never carry secret values")
	}
	raw, present := content["secret_refs"]
	if !present {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("secret_refs must contain credential references")
	}
	var entries map[string]map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &entries); err != nil || entries == nil {
		return fmt.Errorf("secret_refs must contain credential references")
	}
	for key, fields := range entries {
		if fields == nil {
			return fmt.Errorf("secret_refs.%s must be a credential reference", key)
		}
		for field := range fields {
			switch field {
			case "store", "credential_id", "version":
			default:
				return fmt.Errorf("secret_refs.%s accepts only store, credential_id and version", key)
			}
		}
	}
	return nil
}

type adoptionImportEventRequest struct {
	Targets    []adoptionEventTarget    `json:"targets"`
	Selections []adoptionEventSelection `json:"selections,omitempty"`
	ImportAll  bool                     `json:"import_all,omitempty"`
	OrgID      string                   `json:"org_id,omitempty"`
	// NonSecretEnvKeys are environment keys the operator declares are not
	// secrets (adoption keeps them instead of refusing the container).
	NonSecretEnvKeys []string                              `json:"non_secret_env_keys,omitempty"`
	SecretRefs       map[string]domain.SecretCredentialRef `json:"secret_refs,omitempty"`
}

func validateAdoptionSecretRefs(refs map[string]domain.SecretCredentialRef, nonSecret []string) error {
	if len(refs) > maxNonSecretEnvKeys {
		return fmt.Errorf("secret_refs lists too many keys")
	}
	exempt := make(map[string]bool, len(nonSecret))
	for _, key := range nonSecret {
		exempt[key] = true
	}
	for key, ref := range refs {
		if strings.TrimSpace(key) != key || key == "" || len(key) > 256 || strings.ContainsAny(key, "=\x00\r\n") {
			return fmt.Errorf("secret_refs keys must be environment variable names")
		}
		if exempt[key] {
			return fmt.Errorf("secret_refs and non_secret_env_keys both name %s", key)
		}
		if err := service.ValidateAdoptionCredentialRef(ref); err != nil {
			return fmt.Errorf("secret_refs.%s: invalid credential reference", key)
		}
	}
	return nil
}

// maxNonSecretEnvKeys bounds non_secret_env_keys.
const maxNonSecretEnvKeys = 256

func validateNonSecretEnvKeys(keys []string) error {
	if len(keys) > maxNonSecretEnvKeys {
		return fmt.Errorf("non_secret_env_keys lists %d keys; at most %d", len(keys), maxNonSecretEnvKeys)
	}
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if strings.TrimSpace(key) != key || key == "" || len(key) > 256 || strings.ContainsAny(key, "=\x00\r\n") {
			return fmt.Errorf("non_secret_env_keys entries must be environment variable names")
		}
		if _, dup := seen[key]; dup {
			return fmt.Errorf("non_secret_env_keys lists %q twice", key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

type adoptionEventTarget struct {
	Name            string  `json:"name"`
	EndpointRef     string  `json:"endpoint_ref"`
	EnvironmentName string  `json:"environment_name,omitempty"`
	EnvironmentID   string  `json:"environment_id,omitempty"`
	DockerHost      *string `json:"docker_host,omitempty"`
}

type adoptionEventSelection struct {
	TargetName          string `json:"target_name"`
	ContainerID         string `json:"container_id"`
	ServiceNameOverride string `json:"service_name_override,omitempty"`
	ServiceID           string `json:"service_id,omitempty"`
}

func mapAdoptionEventTargets(targets []adoptionEventTarget, requireIDs bool) ([]service.AdoptionTarget, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("at least one target is required")
	}
	mapped := make([]service.AdoptionTarget, 0, len(targets))
	seen := map[string]struct{}{}
	for _, target := range targets {
		if target.DockerHost != nil {
			return nil, fmt.Errorf("target docker_host is forbidden for signer-first adoption; use endpoint_ref")
		}
		name := normalizeAdoptionEventName(target.Name)
		if name == "" {
			return nil, fmt.Errorf("target name is required")
		}
		if _, ok := seen[name]; ok {
			return nil, fmt.Errorf("target names must be unique after normalization")
		}
		seen[name] = struct{}{}
		endpointRef := strings.TrimSpace(target.EndpointRef)
		if endpointRef == "" {
			return nil, fmt.Errorf("target endpoint_ref is required for signer-first adoption")
		}
		environmentName := normalizeAdoptionEventName(target.EnvironmentName)
		if strings.TrimSpace(target.EnvironmentName) != "" && environmentName == "" {
			return nil, fmt.Errorf("target environment_name is invalid")
		}
		var environmentID uuid.UUID
		if requireIDs {
			var err error
			environmentID, err = parseAdoptionUUIDv7(target.EnvironmentID, "target environment_id")
			if err != nil {
				return nil, err
			}
		}
		mapped = append(mapped, service.AdoptionTarget{Name: name, EndpointRef: endpointRef, EnvironmentName: environmentName, EnvironmentID: environmentID})
	}
	return mapped, nil
}

func mapAdoptionEventSelections(selections []adoptionEventSelection) ([]service.AdoptionSelection, error) {
	mapped := make([]service.AdoptionSelection, 0, len(selections))
	seen := map[string]struct{}{}
	for _, selection := range selections {
		targetName := normalizeAdoptionEventName(selection.TargetName)
		containerID := strings.TrimSpace(selection.ContainerID)
		if targetName == "" {
			return nil, fmt.Errorf("selection target_name is required")
		}
		if containerID == "" {
			return nil, fmt.Errorf("selection container_id is required")
		}
		serviceNameOverride := normalizeAdoptionEventName(selection.ServiceNameOverride)
		if strings.TrimSpace(selection.ServiceNameOverride) != "" && serviceNameOverride == "" {
			return nil, fmt.Errorf("selection service_name_override is invalid")
		}
		key := targetName + "/" + containerID
		if _, ok := seen[key]; ok {
			return nil, fmt.Errorf("selection entries must be unique")
		}
		seen[key] = struct{}{}
		serviceID, err := parseAdoptionUUIDv7(selection.ServiceID, "selection service_id")
		if err != nil {
			return nil, err
		}
		mapped = append(mapped, service.AdoptionSelection{TargetName: targetName, ContainerID: containerID, ServiceNameOverride: serviceNameOverride, ServiceID: serviceID})
	}
	return mapped, nil
}

var invalidAdoptionEventNameChars = regexp.MustCompile(`[^a-z0-9-]+`)

func normalizeAdoptionEventName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = invalidAdoptionEventNameChars.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	return name
}

func parseAdoptionUUIDv7(raw, field string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil || id.Version() != 7 || id.String() != raw || id.Variant() != uuid.RFC4122 {
		return uuid.Nil, fmt.Errorf("%s must be a canonical UUIDv7", field)
	}
	return id, nil
}
