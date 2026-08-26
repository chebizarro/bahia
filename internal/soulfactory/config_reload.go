package soulfactory

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
)

const SoulFactoryConfigReloadSchema = "soulfactory-config-reload/v1"

var configReloadSections = map[string]struct{}{
	"identity":     {},
	"persona":      {},
	"avatar":       {},
	"voice":        {},
	"memory":       {},
	"runtime":      {},
	"permissions":  {},
	"relay_policy": {},
	"workspace":    {},
	"assets":       {},
	"fleet_config": {},
}

// ConfigReloadRequest is the params contract for kind:38384
// soulfactory.config.reload requests. It supports partial patches and fully
// resolved draft specs; runtimes use TargetFields to dispatch native hot-reload
// handlers without restarting the session.
type ConfigReloadRequest struct {
	Schema           string                   `json:"schema,omitempty"`
	TargetFields     []string                 `json:"target_fields,omitempty"`
	Patch            map[string]interface{}   `json:"patch,omitempty"`
	ResolvedSpec     *domain.SoulDraftContent `json:"resolved_spec,omitempty"`
	PreviousSpecHash string                   `json:"previous_spec_hash,omitempty"`
	NewSpecHash      string                   `json:"new_spec_hash,omitempty"`
	DraftRef         string                   `json:"draft_ref,omitempty"`
	DraftEventID     string                   `json:"draft_event_id,omitempty"`
}

func ParseConfigReloadRequest(params map[string]interface{}) (ConfigReloadRequest, error) {
	if params == nil {
		params = map[string]interface{}{}
	}
	body, err := json.Marshal(params)
	if err != nil {
		return ConfigReloadRequest{}, fmt.Errorf("marshal reload params: %w", err)
	}
	var req ConfigReloadRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return ConfigReloadRequest{}, fmt.Errorf("parse reload params: %w", err)
	}
	if strings.TrimSpace(req.Schema) == "" {
		req.Schema = SoulFactoryConfigReloadSchema
	}
	if req.Schema != SoulFactoryConfigReloadSchema && req.Schema != domain.SoulFactoryDraftSchemaV2 && req.Schema != domain.SoulFactoryDraftSchemaLatest {
		return ConfigReloadRequest{}, fmt.Errorf("unsupported reload schema %q", req.Schema)
	}
	req.TargetFields = append(req.TargetFields, stringSliceParam(params["target"])...)
	req.TargetFields = append(req.TargetFields, stringSliceParam(params["targets"])...)
	if req.Patch == nil {
		req.Patch = map[string]interface{}{}
	}
	req.TargetFields = normalizeConfigReloadTargets(req.TargetFields)
	if len(req.TargetFields) == 0 {
		req.TargetFields = inferConfigReloadTargets(req.Patch, req.ResolvedSpec)
	}
	if len(req.Patch) == 0 && req.ResolvedSpec == nil {
		return ConfigReloadRequest{}, fmt.Errorf("config reload requires patch or resolved_spec")
	}
	if len(req.TargetFields) == 0 {
		return ConfigReloadRequest{}, fmt.Errorf("config reload requires target_fields or changed top-level spec fields")
	}
	return req, nil
}

func stringSliceParam(value interface{}) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []string:
		return append([]string{}, typed...)
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func normalizeConfigReloadTargets(values []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "target" || value == "targets" {
			continue
		}
		if _, ok := configReloadSections[value]; !ok {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func inferConfigReloadTargets(patch map[string]interface{}, resolved *domain.SoulDraftContent) []string {
	seen := map[string]struct{}{}
	for key := range patch {
		key = strings.ToLower(strings.TrimSpace(key))
		if _, ok := configReloadSections[key]; ok {
			seen[key] = struct{}{}
		}
	}
	if resolved != nil {
		if resolved.Identity != (domain.SoulIdentitySpec{}) {
			seen["identity"] = struct{}{}
		}
		if len(resolved.Persona.Traits) > 0 || resolved.Persona.Style != "" || resolved.Persona.Tone != "" || len(resolved.Persona.Constraints) > 0 || len(resolved.Persona.SystemPromptSections) > 0 {
			seen["persona"] = struct{}{}
		}
		if resolved.Avatar.Generation != nil || resolved.Avatar.UploadedRef != "" || resolved.Avatar.GeneratedRef != "" || resolved.Avatar.Current != "" {
			seen["avatar"] = struct{}{}
		}
		if resolved.Voice.Provider != "" || resolved.Voice.PersonaID != "" || resolved.Voice.Persona != nil || resolved.Voice.AutoMode != "" || resolved.Voice.SampleText != "" || len(resolved.Voice.Providers) > 0 {
			seen["voice"] = struct{}{}
		}
		if resolved.Memory.EmbeddingProvider != "" || resolved.Memory.EmbeddingModel != "" || resolved.Memory.Search != nil || resolved.Memory.Strategy != "" || resolved.Memory.AutoIndex || resolved.Memory.RetentionDays != 0 {
			seen["memory"] = struct{}{}
		}
		if resolved.Runtime != (domain.SoulRuntimeSpec{}) {
			seen["runtime"] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for key := range seen {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
