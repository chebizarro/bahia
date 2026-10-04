package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/pkg/client"
)

// Registry mutations take the full desired state in content. The typed
// domain objects below are the same JSON shapes accepted by the daemon's ML
// and DNS intent handlers (see d70-intent-content.json).
func registryIntentToolDefinitions() []Tool {
	var tools []Tool
	for _, spec := range []struct{ name, description string }{
		{"bahia_ml_model_create", "Create an ML model registry record"},
		{"bahia_ml_model_update", "Update an ML model registry record"},
		{"bahia_ml_model_delete", "Delete an ML model registry record"},
		{"bahia_ml_version_create", "Create an ML model version"},
		{"bahia_ml_version_update", "Update an ML model version"},
		{"bahia_ml_version_delete", "Delete an ML model version"},
		{"bahia_ml_endpoint_create", "Create an ML inference endpoint"},
		{"bahia_ml_endpoint_update", "Update an ML inference endpoint"},
		{"bahia_ml_endpoint_delete", "Delete an ML inference endpoint"},
		{"bahia_assistant_dns_zone_create", "Create a managed DNS zone"},
		{"bahia_assistant_dns_zone_update", "Update a managed DNS zone"},
		{"bahia_assistant_dns_zone_delete", "Delete a managed DNS zone"},
		{"bahia_assistant_dns_endpoint_create", "Create a DNS endpoint"},
		{"bahia_assistant_dns_endpoint_update", "Update a DNS endpoint"},
		{"bahia_assistant_dns_endpoint_delete", "Delete a DNS endpoint"},
		{"bahia_assistant_dns_backend_create", "Create a DNS backend"},
		{"bahia_assistant_dns_backend_update", "Update a DNS backend"},
		{"bahia_assistant_dns_backend_delete", "Delete a DNS backend"},
		{"bahia_assistant_dns_policy_create", "Create a DNS policy"},
		{"bahia_assistant_dns_policy_apply", "Apply a DNS policy"},
		{"bahia_assistant_dns_policy_update", "Update a DNS policy"},
		{"bahia_assistant_dns_policy_delete", "Delete a DNS policy"},
		{"bahia_assistant_dns_record_set", "Set a DNS record override"},
		{"bahia_assistant_dns_record_override", "Set a DNS record override"},
		{"bahia_assistant_dns_override_retire", "Retire a DNS record override"},
	} {
		tools = append(tools, Tool{Name: spec.name, Description: spec.description, InputSchema: objectSchema(map[string]interface{}{
			"org_id":          stringProp,
			"content":         map[string]interface{}{"type": "object", "description": "Full desired state accepted by the daemon intent handler"},
			"id":              stringProp,
			"idempotency_key": stringProp,
		}, "org_id", "content")})
	}
	return tools
}

func isRegistryIntentTool(name string) bool {
	for _, tool := range registryIntentToolDefinitions() {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func decodeMCPStateCoordinate(event nostr.Event) (string, error) {
	decoded, err := client.DecodeControlStateEvent(event)
	if err != nil {
		return "", err
	}
	return decoded.DTag, nil
}

func (s *Server) replayedRegistryDelete(ctx context.Context, name string, args map[string]any, intentID string, replay *controlplane.ProcessedIntentRecord) (*ToolResult, bool) {
	input, err := registryIntentContent(args)
	if err != nil {
		return intentWriteError("rejected", intentID, replay.EventID, err.Error()), true
	}
	var family int
	var domainName, coordinate, identityKey, identity string
	if strings.HasPrefix(name, "bahia_ml_") {
		domainName = "ml"
		identityKey, identity = "id", stringArg(input, "id")
		switch name {
		case "bahia_ml_model_delete":
			family = kinds.MLModelRegistry
			if slug := stringArg(input, "slug"); slug != "" {
				coordinate = "model:" + slug
			}
		case "bahia_ml_version_delete":
			family = kinds.MLModelVersionRegistry
			coordinate = "model-version:" + identity
		case "bahia_ml_endpoint_delete":
			family = kinds.MLInferenceEndpointRegistry
			coordinate = "endpoint:" + identity
		}
	} else {
		domainName = "dns"
		switch name {
		case "bahia_assistant_dns_zone_delete":
			family, identityKey, identity = kinds.CPStateFamilyDNSZone.LegacyKind(), "name", stringArg(input, "name")
			coordinate = "zone:" + identity
		case "bahia_assistant_dns_endpoint_delete":
			family, identityKey, identity = kinds.CPStateFamilyDNSEndpoint.LegacyKind(), "coordinate", stringArg(input, "coordinate")
			coordinate = identity
		case "bahia_assistant_dns_backend_delete":
			family, identityKey, identity = kinds.CPStateFamilyDNSBackend.LegacyKind(), "ref", stringArg(input, "ref")
			coordinate = "dnsbackend:" + identity
		case "bahia_assistant_dns_policy_delete":
			family, identityKey, identity = kinds.CPStateFamilyDNSPolicy.LegacyKind(), "id", stringArg(input, "id")
			coordinate = "dnspolicy:" + identity
		}
	}
	if family == 0 || identity == "" {
		return intentWriteError("rejected", intentID, replay.EventID, "deletion identity is required"), true
	}
	if replay.Domain != domainName || replay.Op != strings.ReplaceAll(strings.TrimPrefix(strings.TrimPrefix(name, "bahia_assistant_dns_"), "bahia_ml_"), "_", "-") || (coordinate != "" && replay.Coordinate != coordinate) {
		return intentWriteError("conflict", intentID, replay.EventID, "idempotency key reused for a different deletion"), true
	}
	deleted, err := s.hasCanonicalTombstoneMatching(ctx, family, identityKey, identity)
	if err != nil {
		return intentWriteError("error", intentID, replay.EventID, err.Error()), true
	}
	result := map[string]any{"status": "pending", "intent_id": intentID, "event_id": replay.EventID}
	if deleted {
		result["status"], result["deleted"], result[identityKey] = "accepted", true, identity
	}
	toolResult, _ := jsonResult(result)
	return toolResult, true
}

func (s *Server) hasCanonicalTombstoneMatching(ctx context.Context, family int, key, value string) (bool, error) {
	pubkey, err := nostr.PubKeyFromHex(s.servicePubkey)
	if err != nil {
		return false, err
	}
	for _, topic := range nostrpool.CPStateFamilyTopics() {
		if topic.LegacyKind != family {
			continue
		}
		filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Authors: []nostr.PubKey{pubkey}, Tags: nostr.TagMap{"t": {topic.Topic}}}
		for event := range s.stateStore.QueryEvents(filter) {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			if event.PubKey != pubkey || !event.CheckID() || !event.VerifySignature() {
				return false, fmt.Errorf("invalid signed cp-state event %s", event.ID.Hex())
			}
			decoded, err := client.DecodeControlStateEvent(event)
			if err != nil {
				return false, err
			}
			if !decoded.Deleted || decoded.LegacyKind != family {
				continue
			}
			var content map[string]any
			if err := json.Unmarshal(decoded.Content, &content); err != nil {
				return false, err
			}
			if content[key] == value {
				return true, nil
			}
		}
		return false, nil
	}
	return false, fmt.Errorf("unknown cp-state family %d", family)
}

func registryIntentContent(args map[string]any) (map[string]any, error) {
	raw, ok := args["content"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("content must be an object containing the full desired state")
	}
	out := copyIntentFields(raw)
	return out, nil
}

func typedRegistryIntentContent(input map[string]any, target any) (map[string]any, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, target); err != nil {
		return nil, err
	}
	content, err := typedIntentContent(target)
	if err != nil {
		return nil, err
	}
	if expected, ok := input["expected_updated_at"]; ok {
		content["expected_updated_at"] = expected
	}
	return content, nil
}

func (s *Server) registryIntentWrite(ctx context.Context, name string, args map[string]any, intentID string) (intentWrite, error) {
	w := intentWrite{}
	var err error
	w.orgID, err = s.intentOrgFromState(ctx, args, 0, "", "")
	if err != nil {
		return w, err
	}
	input, err := registryIntentContent(args)
	if err != nil {
		return w, err
	}
	if strings.HasPrefix(name, "bahia_ml_") {
		w.domain = "ml"
		operation := strings.TrimPrefix(name, "bahia_ml_")
		switch operation {
		case "model_create", "model_update":
			var model domain.MLModel
			if operation == "model_create" && input["id"] == nil {
				input["id"] = intentID
			}
			w.content, err = typedRegistryIntentContent(input, &model)
			if err != nil {
				return w, err
			}
			if model.ID == uuid.Nil || strings.TrimSpace(model.Slug) == "" {
				return w, fmt.Errorf("model id and slug are required")
			}
			w.op = strings.ReplaceAll(operation, "_", "-")
			w.coordinate, w.family, w.stateKey, w.stateValue = "model:"+strings.TrimSpace(model.Slug), kinds.MLModelRegistry, "id", model.ID.String()
		case "version_create", "version_update":
			var version domain.MLModelVersion
			if operation == "version_create" && input["id"] == nil {
				input["id"] = intentID
			}
			w.content, err = typedRegistryIntentContent(input, &version)
			if err != nil {
				return w, err
			}
			if version.ID == uuid.Nil {
				return w, fmt.Errorf("model version id is required")
			}
			w.op = strings.ReplaceAll(operation, "_", "-")
			w.coordinate, w.family, w.stateKey, w.stateValue = "model-version:"+version.ID.String(), kinds.MLModelVersionRegistry, "id", version.ID.String()
		case "endpoint_create", "endpoint_update":
			var endpoint domain.MLInferenceEndpoint
			if operation == "endpoint_create" && input["id"] == nil {
				input["id"] = intentID
			}
			w.content, err = typedRegistryIntentContent(input, &endpoint)
			if err != nil {
				return w, err
			}
			if endpoint.ID == uuid.Nil {
				return w, fmt.Errorf("ML endpoint id is required")
			}
			w.op = strings.ReplaceAll(operation, "_", "-")
			w.coordinate, w.family, w.stateKey, w.stateValue = "endpoint:"+endpoint.ID.String(), kinds.MLInferenceEndpointRegistry, "id", endpoint.ID.String()
		case "model_delete", "version_delete", "endpoint_delete":
			id, e := uuid.Parse(stringArg(input, "id"))
			if e != nil || id == uuid.Nil {
				return w, fmt.Errorf("id must be a non-nil UUID")
			}
			var family int
			switch operation {
			case "model_delete":
				family = kinds.MLModelRegistry
			case "version_delete":
				family = kinds.MLModelVersionRegistry
			default:
				family = kinds.MLInferenceEndpointRegistry
			}
			record, e := s.readStateOne(ctx, family, "id", id.String())
			if e != nil {
				return w, e
			}
			if record == nil {
				return w, fmt.Errorf("canonical ML record %s not found", id)
			}
			w.op = strings.ReplaceAll(operation, "_", "-")
			w.family, w.stateKey, w.stateValue, w.deleted = family, "id", id.String(), true
			switch operation {
			case "model_delete":
				w.coordinate = "model:" + stringFromRecord(record.Fields, "slug")
			case "version_delete":
				w.coordinate = "model-version:" + id.String()
			default:
				w.coordinate = "endpoint:" + id.String()
			}
			w.content = map[string]any{"id": id.String()}
			if expected, ok := input["expected_updated_at"]; ok {
				w.content["expected_updated_at"] = expected
			}
			// The ML projector uses a human coordinate as its replaceable d-tag,
			// while the handler addresses versions/endpoints by UUID.
			if decoded, e := decodeMCPStateCoordinate(record.Event); e == nil {
				w.deleteCoordinate = decoded
			}
		default:
			return w, fmt.Errorf("unsupported ML registry tool %q", name)
		}
		return w, nil
	}
	w.domain = "dns"
	operation := strings.TrimPrefix(name, "bahia_assistant_dns_")
	switch operation {
	case "zone_create", "zone_update":
		var zone domain.DNSZone
		w.content, err = typedRegistryIntentContent(input, &zone)
		if err != nil {
			return w, err
		}
		if zone.Name == "" {
			return w, fmt.Errorf("DNS zone name is required")
		}
		w.op = strings.ReplaceAll(operation, "_", "-")
		w.coordinate, w.family, w.stateKey, w.stateValue = "zone:"+strings.TrimSpace(zone.Name), kinds.CPStateFamilyDNSZone.LegacyKind(), "name", strings.TrimSpace(zone.Name)
	case "zone_delete":
		name := strings.TrimSpace(stringArg(input, "name"))
		if name == "" {
			return w, fmt.Errorf("DNS zone name is required")
		}
		w.op, w.coordinate, w.content = "zone-delete", "zone:"+name, map[string]any{"name": name}
		w.family, w.stateKey, w.stateValue, w.deleted, w.deleteCoordinate = kinds.CPStateFamilyDNSZone.LegacyKind(), "name", name, true, "zone:"+name
	case "endpoint_create", "endpoint_update":
		var endpoint domain.DNSEndpoint
		w.content, err = typedRegistryIntentContent(input, &endpoint)
		if err != nil {
			return w, err
		}
		if endpoint.Coordinate == "" {
			return w, fmt.Errorf("DNS endpoint coordinate is required")
		}
		w.op = strings.ReplaceAll(operation, "_", "-")
		w.coordinate, w.family, w.stateKey, w.stateValue = endpoint.Coordinate, kinds.CPStateFamilyDNSEndpoint.LegacyKind(), "coordinate", endpoint.Coordinate
	case "endpoint_delete":
		coordinate := strings.TrimSpace(stringArg(input, "coordinate"))
		if coordinate == "" {
			return w, fmt.Errorf("DNS endpoint coordinate is required")
		}
		w.op, w.coordinate, w.content = "endpoint-delete", coordinate, map[string]any{"coordinate": coordinate}
		w.family, w.stateKey, w.stateValue, w.deleted, w.deleteCoordinate = kinds.CPStateFamilyDNSEndpoint.LegacyKind(), "coordinate", coordinate, true, coordinate
	case "backend_create", "backend_update":
		var backend domain.DNSBackendState
		w.content, err = typedRegistryIntentContent(input, &backend)
		if err != nil {
			return w, err
		}
		if backend.Ref == "" {
			return w, fmt.Errorf("DNS backend ref is required")
		}
		w.op = strings.ReplaceAll(operation, "_", "-")
		w.coordinate, w.family, w.stateKey, w.stateValue = "dnsbackend:"+strings.TrimSpace(backend.Ref), kinds.CPStateFamilyDNSBackend.LegacyKind(), "ref", strings.TrimSpace(backend.Ref)
	case "backend_delete":
		ref := strings.TrimSpace(stringArg(input, "ref"))
		if ref == "" {
			return w, fmt.Errorf("DNS backend ref is required")
		}
		w.op, w.coordinate, w.content = "backend-delete", "dnsbackend:"+ref, map[string]any{"ref": ref}
		w.family, w.stateKey, w.stateValue, w.deleted, w.deleteCoordinate = kinds.CPStateFamilyDNSBackend.LegacyKind(), "ref", ref, true, "dnsbackend:"+ref
	case "policy_create", "policy_apply", "policy_update":
		var policy domain.DNSPolicy
		if (operation == "policy_create" || operation == "policy_apply") && input["id"] == nil {
			input["id"] = intentID
		}
		w.content, err = typedRegistryIntentContent(input, &policy)
		if err != nil {
			return w, err
		}
		if policy.ID == uuid.Nil {
			return w, fmt.Errorf("DNS policy id is required")
		}
		w.op = "policy-update"
		if operation == "policy_create" || operation == "policy_apply" {
			w.op = "policy-apply"
		}
		w.coordinate, w.family, w.stateKey, w.stateValue = "dnspolicy:"+policy.ID.String(), kinds.CPStateFamilyDNSPolicy.LegacyKind(), "id", policy.ID.String()
	case "policy_delete":
		id, e := uuid.Parse(stringArg(input, "id"))
		if e != nil || id == uuid.Nil {
			return w, fmt.Errorf("DNS policy id must be a non-nil UUID")
		}
		w.op, w.coordinate, w.content = "policy-delete", "dnspolicy:"+id.String(), map[string]any{"id": id.String()}
		w.family, w.stateKey, w.stateValue, w.deleted, w.deleteCoordinate = kinds.CPStateFamilyDNSPolicy.LegacyKind(), "id", id.String(), true, "dnspolicy:"+id.String()
	case "record_set", "record_override":
		var override domain.DNSRecordOverride
		if input["id"] == nil {
			input["id"] = intentID
		}
		w.content, err = typedRegistryIntentContent(input, &override)
		if err != nil {
			return w, err
		}
		if override.ID == uuid.Nil {
			return w, fmt.Errorf("DNS override id is required")
		}
		w.op, w.coordinate = "record-set", "dns-override:"+override.ID.String()
		w.family, w.stateMatch = kinds.CPStateFamilyDNSEndpoint.LegacyKind(), map[string]string{"zone": override.ZoneName}
	case "override_retire":
		id, e := uuid.Parse(stringArg(input, "override_id"))
		if e != nil || id == uuid.Nil {
			return w, fmt.Errorf("DNS override_id must be a non-nil UUID")
		}
		w.op, w.coordinate = "override-retire", "dns-override:"+id.String()
		w.content = map[string]any{"override_id": id.String(), "reason": stringArg(input, "reason")}
		if zone := strings.TrimSpace(stringArg(input, "zone_name")); zone != "" {
			w.family, w.stateMatch = kinds.CPStateFamilyDNSEndpoint.LegacyKind(), map[string]string{"zone": zone}
		}
	default:
		return w, fmt.Errorf("unsupported DNS registry tool %q", name)
	}
	if expected, ok := input["expected_updated_at"]; ok {
		w.content["expected_updated_at"] = expected
	}
	return w, nil
}
