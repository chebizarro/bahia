package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/pkg/client"
)

// intentWrite describes one in-process mutation and the canonical record it
// should leave behind. A missing record after successful processing is pending,
// not an accepted read of stale repository state.
type intentWrite struct {
	domain, op, coordinate string
	orgID                  uuid.UUID
	content                map[string]any
	family                 int
	stateKey, stateValue   string
	stateMatch             map[string]string
	deleted                bool
	deleteCoordinate       string
}

// invokeIntentWrite makes direct handler calls obey the same pipeline as CallTool.
// There is no legacy publisher fallback when the processor is unavailable.
func (s *Server) invokeIntentWrite(ctx context.Context, name string, args map[string]interface{}) (*ToolResult, error) {
	if result, ok := s.callIntentWrite(ctx, name, args); ok {
		return result, nil
	}
	return intentWriteError("rejected", "", "", "intent processor is not configured"), nil
}

// callIntentWrite is the only MCP transport for intent-backed mutations. The
// event is built by the same builder used by the CLI/web fixtures; it is not
// published or signed, because ProcessInProcess authenticates the HTTP caller
// and uses its pubkey for TrustSet authorization.
func (s *Server) callIntentWrite(ctx context.Context, name string, args map[string]interface{}) (*ToolResult, bool) {
	if s.intentProc == nil {
		return nil, false
	}
	if !isIntentWriteTool(name) {
		return nil, false
	}
	principal := auth.GetPrincipal(ctx)
	if principal == nil || !principal.IsAuthenticated() {
		return intentWriteError("rejected", "", "", "authentication required"), true
	}
	actor := strings.ToLower(strings.TrimSpace(principal.PubKey))
	if _, err := nostr.PubKeyFromHex(actor); err != nil {
		return intentWriteError("rejected", "", "", "MCP write requires an authenticated Nostr pubkey"), true
	}
	intentID, err := mcpIntentID(name, actor, args)
	if err != nil {
		return intentWriteError("rejected", "", "", err.Error()), true
	}
	replay := s.intentProc.ProcessedIntent(intentID)
	if replay != nil {
		if replay.Actor != actor {
			return intentWriteError("conflict", intentID, replay.EventID, "idempotency key belongs to a different actor"), true
		}
		if result, handled := s.replayedDeleteIntent(ctx, name, args, intentID, replay); handled {
			return result, true
		}
	}
	write, err := s.intentWriteForTool(ctx, name, args, intentID)
	if err != nil {
		return intentWriteError("rejected", intentID, "", err.Error()), true
	}
	if s.intentProc.Handler(write.domain) == nil {
		return intentWriteError("rejected", intentID, "", "intent domain "+write.domain+" is not configured"), true
	}
	if replay != nil && (replay.Domain != write.domain || replay.Op != write.op || replay.Coordinate != write.coordinate) {
		return intentWriteError("conflict", intentID, replay.EventID, "idempotency key reused for a different intent"), true
	}
	write.content["intent_id"] = intentID
	req := client.PublishIntentRequest{
		Domain: write.domain, Op: write.op, Coordinate: write.coordinate,
		OrgID: write.orgID.String(), Content: write.content, IntentID: intentID,
	}
	ev, err := (&client.IntentPublisher{}).BuildIntentEvent(req)
	if err != nil {
		return intentWriteError("rejected", intentID, "", err.Error()), true
	}
	ev.PubKey, _ = nostr.PubKeyFromHex(actor)
	ev.ID = ev.GetID()
	eventID := ev.ID.Hex()
	if replay != nil && replay.EventID != "" {
		eventID = replay.EventID
	}
	intent, err := controlplane.ParseIntent(&ev)
	if err != nil {
		return intentWriteError("rejected", intentID, eventID, err.Error()), true
	}
	intent.Actor = actor
	replayed := replay != nil || s.intentProc.IsProcessed(intentID)
	var priorEventID string
	if write.family != 0 && !write.deleted && write.stateKey != "metadata.nostr_event_id" {
		prior, readErr := s.readIntentWriteState(ctx, write)
		if readErr != nil {
			return intentWriteError("error", intentID, eventID, "read prior canonical state: "+readErr.Error()), true
		}
		if prior != nil {
			priorEventID = prior.Event.ID.Hex()
		}
	}
	if err := s.intentProc.ProcessInProcess(ctx, intent); err != nil {
		status := "rejected"
		if controlplane.IsRevisionConflict(err) {
			status = "conflict"
		}
		return intentWriteError(status, intentID, eventID, err.Error()), true
	}
	result := map[string]any{"status": "pending", "intent_id": intentID, "event_id": eventID}
	if write.deleted {
		coordinate := write.deleteCoordinate
		if coordinate == "" {
			coordinate = write.stateValue
		}
		deleted, readErr := s.hasCanonicalTombstone(ctx, write.family, coordinate)
		if readErr != nil {
			return intentWriteError("error", intentID, eventID, "read canonical tombstone: "+readErr.Error()), true
		}
		if deleted {
			result["status"] = "accepted"
			result["deleted"] = true
			result["id"] = write.stateValue
		}
	} else if write.family != 0 {
		var record *stateRecord
		var readErr error
		if write.stateKey == "metadata.nostr_event_id" {
			record, readErr = s.readStateByRequestEvent(ctx, write.family, eventID)
		} else {
			record, readErr = s.readIntentWriteState(ctx, write)
		}
		if readErr != nil {
			return intentWriteError("error", intentID, eventID, "read canonical state: "+readErr.Error()), true
		}
		if record != nil && (replayed || record.Event.ID.Hex() != priorEventID) {
			result["status"] = "accepted"
			if write.family == nostrpool.KindNotificationChannelRegistry {
				var channel domain.NotificationChannel
				if err := json.Unmarshal(record.Content, &channel); err != nil {
					return intentWriteError("error", intentID, eventID, "decode notification channel: "+err.Error()), true
				}
				result["state"] = notificationChannelToMap(&channel)
			} else {
				result["state"] = record.Fields
			}
		}
	}

	toolResult, _ := jsonResult(result)
	return toolResult, true
}

func (s *Server) readIntentWriteState(ctx context.Context, write intentWrite) (*stateRecord, error) {
	if len(write.stateMatch) == 0 {
		return s.readStateOne(ctx, write.family, write.stateKey, write.stateValue)
	}
	records, err := s.readStateFamily(ctx, write.family)
	if err != nil {
		return nil, err
	}
	for i := range records {
		matched := true
		for key, value := range write.stateMatch {
			if records[i].Fields[key] != value {
				matched = false
				break
			}
		}
		if matched {
			return &records[i], nil
		}
	}
	return nil, nil
}

// A deleted record cannot supply its former org on replay. The processor's
// durable marker binds the completed call to its actor, operation and entity;
// a matching signed tombstone is sufficient to return the original outcome.
func (s *Server) replayedDeleteIntent(ctx context.Context, name string, args map[string]any, intentID string, replay *controlplane.ProcessedIntentRecord) (*ToolResult, bool) {
	var arg, domain string
	var family int
	switch name {
	case "bahia_delete_service":
		arg, domain, family = "service_id", "service", nostrpool.KindServiceRegistry
	case "bahia_delete_environment":
		arg, domain, family = "environment_id", "environment", nostrpool.KindEnvironmentRegistry
	case "bahia_delete_policy":
		arg, domain, family = "policy_id", "policy", nostrpool.KindPolicyRegistry
	case "bahia_delete_secret":
		arg, domain, family = "secret_id", "secret", nostrpool.KindSecretRegistry
	case "bahia_delete_notification_channel":
		arg, domain, family = "channel_id", "notification", nostrpool.KindNotificationChannelRegistry
	default:
		if isRegistryIntentTool(name) && strings.HasSuffix(name, "_delete") {
			return s.replayedRegistryDelete(ctx, name, args, intentID, replay)
		}
		return nil, false
	}
	id, err := parseRequiredUUIDArg(args, arg)
	if err != nil {
		return intentWriteError("rejected", intentID, replay.EventID, err.Error()), true
	}
	if replay.Domain != domain || replay.Op != "delete" || replay.Coordinate != id.String() {
		return intentWriteError("conflict", intentID, replay.EventID, "idempotency key reused for a different deletion"), true
	}
	deleted, err := s.hasCanonicalTombstone(ctx, family, id.String())
	if err != nil {
		return intentWriteError("error", intentID, replay.EventID, err.Error()), true
	}
	status := "pending"
	result := map[string]any{"status": status, "intent_id": intentID, "event_id": replay.EventID}
	if deleted {
		result["status"], result["deleted"], result["id"] = "accepted", true, id.String()
	}
	toolResult, _ := jsonResult(result)
	return toolResult, true
}

func (s *Server) readStateByRequestEvent(ctx context.Context, family int, eventID string) (*stateRecord, error) {
	records, err := s.readStateFamily(ctx, family)
	if err != nil {
		return nil, err
	}
	for i := range records {
		metadata, _ := records[i].Fields["metadata"].(map[string]any)
		if metadata["nostr_event_id"] == eventID {
			return &records[i], nil
		}
	}
	return nil, nil
}

func (s *Server) hasCanonicalTombstone(ctx context.Context, family int, coordinate string) (bool, error) {
	pubkey, err := nostr.PubKeyFromHex(s.servicePubkey)
	if err != nil {
		return false, err
	}
	for _, familyTopic := range nostrpool.CPStateFamilyTopics() {
		if familyTopic.LegacyKind != family {
			continue
		}
		filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Authors: []nostr.PubKey{pubkey}, Tags: nostr.TagMap{"t": {familyTopic.Topic}, "d": {coordinate}}}
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
			if decoded.LegacyKind == family && decoded.DTag == coordinate && decoded.Deleted {
				return true, nil
			}
		}
		return false, nil
	}
	return false, fmt.Errorf("unknown cp-state family %d", family)
}

func intentWriteError(status, intentID, eventID, reason string) *ToolResult {
	result, _ := jsonResult(map[string]any{"status": status, "intent_id": intentID, "event_id": eventID, "reason": reason})
	result.IsError = true
	return result
}

func isIntentWriteTool(name string) bool {
	if backupToolPublishesCommand(backupToolBaseName(name)) {
		return true
	}
	if isRegistryIntentTool(name) {
		return true
	}
	switch name {
	case "bahia_package_repository_apply", "bahia_package_repository_delete", "bahia_package_upload", "bahia_package_promote", "bahia_package_yank", "bahia_package_drift_detect",
		"bahia_create_service", "bahia_update_service", "bahia_delete_service",
		"bahia_create_environment", "bahia_update_environment", "bahia_delete_environment",
		"bahia_deploy", "bahia_create_intent", "bahia_rollback",
		"bahia_approve_deployment", "bahia_approve_intent", "bahia_reject_deployment", "bahia_reject_intent",
		"bahia_create_policy", "bahia_update_policy", "bahia_delete_policy",
		"bahia_worker_cordon", "bahia_worker_uncordon", "bahia_worker_drain", "bahia_worker_undrain",
		"bahia_worker_maintenance_enter", "bahia_worker_maintenance_exit", "bahia_worker_labels_update",
		"bahia_llm_create_route", "bahia_llm_update_route", "bahia_llm_register_release",
		"bahia_llm_deploy", "bahia_llm_rollback", "bahia_llm_approve_deployment", "bahia_llm_reject_deployment",
		"bahia_assistant_llm_deploy", "bahia_assistant_llm_rollback", "bahia_assistant_llm_approve_deployment",
		"bahia_assistant_service_deploy", "bahia_assistant_service_rollback",
		"bahia_create_secret", "bahia_update_secret", "bahia_delete_secret",
		"bahia_create_notification_channel", "bahia_update_notification_channel", "bahia_delete_notification_channel":
		return true
	default:
		return false
	}
}

// A supplied call key is mapped to a UUIDv7-shaped identifier in a stable
// actor/tool namespace. Without one, each invocation gets a fresh UUIDv7.
func mcpIntentID(tool, actor string, args map[string]interface{}) (string, error) {
	key := strings.TrimSpace(stringArg(args, "idempotency_key"))
	if key == "" {
		if meta, ok := args["_meta"].(map[string]any); ok {
			if token, ok := meta["progressToken"]; ok && token != nil {
				key = strings.TrimSpace(fmt.Sprint(token))
			}
		}
	}
	if key == "" {
		id, err := uuid.NewV7()
		if err != nil {
			return "", fmt.Errorf("mint intent id: %w", err)
		}
		return id.String(), nil
	}
	hash := sha256.Sum256([]byte(actor + "\x00" + tool + "\x00" + key))
	var id uuid.UUID
	copy(id[:], hash[:16])
	id[6] = (id[6] & 0x0f) | 0x70
	id[8] = (id[8] & 0x3f) | 0x80
	return id.String(), nil
}

func mcpIntentEntityID(args map[string]interface{}, intentID string) (uuid.UUID, error) {
	if raw := strings.TrimSpace(stringArg(args, "id")); raw != "" {
		id, _, err := domain.ResolveCreateEntityID(raw)
		return id, err
	}
	return uuid.Parse(intentID)
}

func (s *Server) intentOrgFromState(ctx context.Context, args map[string]interface{}, family int, key, value string) (uuid.UUID, error) {
	var explicit uuid.UUID
	if raw := strings.TrimSpace(stringArg(args, "org_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil || id == uuid.Nil {
			return uuid.Nil, fmt.Errorf("org_id must be a non-nil UUID")
		}
		explicit = id
	}
	if value == "" {
		if explicit != uuid.Nil {
			return explicit, nil
		}
		return uuid.Nil, fmt.Errorf("org_id is required")
	}
	record, err := s.readStateOne(ctx, family, key, value)
	if err != nil {
		return uuid.Nil, err
	}
	if record == nil {
		return uuid.Nil, fmt.Errorf("canonical owner not found; org_id is required")
	}
	id, err := uuid.Parse(stringFromRecord(record.Fields, "org_id"))
	if err == nil && id != uuid.Nil {
		if explicit != uuid.Nil && explicit != id {
			return uuid.Nil, fmt.Errorf("org_id does not match canonical owner")
		}
		return id, nil
	}
	if environmentID := stringFromRecord(record.Fields, "environment_id"); environmentID != "" && family != nostrpool.KindEnvironmentRegistry {
		return s.intentOrgFromState(ctx, args, nostrpool.KindEnvironmentRegistry, "id", environmentID)
	}
	if serviceID := stringFromRecord(record.Fields, "service_id"); serviceID != "" && family != nostrpool.KindServiceRegistry {
		return s.intentOrgFromState(ctx, args, nostrpool.KindServiceRegistry, "id", serviceID)
	}
	if explicit != uuid.Nil {
		return explicit, nil
	}
	return uuid.Nil, fmt.Errorf("canonical owner has no org_id")
}

func typedIntentContent(value any) (map[string]any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var content map[string]any
	if err := json.Unmarshal(encoded, &content); err != nil {
		return nil, err
	}
	return content, nil
}

func copyIntentFields(fields map[string]any) map[string]any {
	out := make(map[string]any, len(fields))
	for k, v := range fields {
		out[k] = v
	}
	return out
}

func (s *Server) existingIntentState(ctx context.Context, family int, id string) (map[string]any, error) {
	record, err := s.readStateOne(ctx, family, "id", id)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, fmt.Errorf("canonical state %s not found", id)
	}
	return copyIntentFields(record.Fields), nil
}

func (s *Server) resolveRollbackDefaults(ctx context.Context, serviceID, envID uuid.UUID, content map[string]any) error {
	if content["supersedes_intent_id"] != nil && (content["target_artifact_id"] != nil || content["target_run_id"] != nil) {
		return nil
	}
	records, err := s.readStateFamily(ctx, nostrpool.KindDeploymentIntentRegistry)
	if err != nil {
		return err
	}
	var latest, previous *stateRecord
	for i := range records {
		record := &records[i]
		if record.Fields["service_id"] != serviceID.String() || record.Fields["environment_id"] != envID.String() {
			continue
		}
		if latest == nil || canonicalRecordNewer(record, latest) {
			latest = record
		}
	}
	if latest == nil {
		return fmt.Errorf("rollback requires a canonical deployment to supersede")
	}
	if content["supersedes_intent_id"] == nil {
		content["supersedes_intent_id"] = stringFromRecord(latest.Fields, "id")
	}
	if content["target_artifact_id"] != nil || content["target_run_id"] != nil {
		return nil
	}
	for i := range records {
		record := &records[i]
		if record.Fields["service_id"] != serviceID.String() || record.Fields["environment_id"] != envID.String() || record.Fields["status"] != string(domain.IntentStatusDeployed) || record.Fields["artifact_id"] == latest.Fields["artifact_id"] {
			continue
		}
		if previous == nil || canonicalRecordNewer(record, previous) {
			previous = record
		}
	}
	if previous == nil {
		return fmt.Errorf("rollback requires a previously deployed artifact")
	}
	content["target_artifact_id"] = stringFromRecord(previous.Fields, "artifact_id")
	return nil
}

func canonicalRecordNewer(a, b *stateRecord) bool {
	at, aok := recordTime(a.Fields, "created_at")
	bt, bok := recordTime(b.Fields, "created_at")
	if aok && bok && !at.Equal(bt) {
		return at.After(bt)
	}
	if a.Event.CreatedAt != b.Event.CreatedAt {
		return a.Event.CreatedAt > b.Event.CreatedAt
	}
	return a.Event.ID.Hex() > b.Event.ID.Hex()
}

func (s *Server) intentWriteForTool(ctx context.Context, name string, args map[string]interface{}, intentID string) (intentWrite, error) {
	if isRegistryIntentTool(name) {
		return s.registryIntentWrite(ctx, name, args, intentID)
	}
	if backupToolPublishesCommand(backupToolBaseName(name)) {
		return s.backupIntentWrite(ctx, backupToolBaseName(name), args, intentID)
	}
	if strings.HasPrefix(name, "bahia_package_") {
		return s.packageIntentWrite(ctx, name, args, intentID)
	}
	var w intentWrite
	var err error
	switch name {
	case "bahia_create_service":
		id, e := mcpIntentEntityID(args, intentID)
		if e != nil {
			return w, e
		}
		w = intentWrite{domain: "service", op: "create", coordinate: id.String(), family: nostrpool.KindServiceRegistry, stateKey: "id", stateValue: id.String(), content: map[string]any{"id": id.String()}}
		w.orgID, err = s.intentOrgFromState(ctx, args, 0, "", "")
		if err != nil {
			return w, err
		}
		for _, key := range []string{"name", "repo_url", "repository", "artifact_repo", "default_branch", "runtime_type"} {
			if value, ok := args[key]; ok {
				w.content[key] = value
			}
		}
		if value, ok := args["managed_runtime_config"]; ok {
			w.content["runtime_config"] = value
		}
		w.content["org_id"] = w.orgID.String()
	case "bahia_update_service", "bahia_delete_service":
		id, e := parseRequiredUUIDArg(args, "service_id")
		if e != nil {
			return w, e
		}
		w = intentWrite{domain: "service", coordinate: id.String(), family: nostrpool.KindServiceRegistry, stateKey: "id", stateValue: id.String()}
		w.orgID, err = s.intentOrgFromState(ctx, args, w.family, "id", id.String())
		if err != nil {
			return w, err
		}
		if name == "bahia_delete_service" {
			w.op, w.deleted, w.content = "delete", true, map[string]any{"id": id.String(), "force": boolArg(args, "force")}
		} else {
			w.op = "update"
			w.content, err = s.existingIntentState(ctx, w.family, id.String())
			if err != nil {
				return w, err
			}
			for _, key := range []string{"name", "org_id", "repo_url", "repository", "artifact_repo", "default_branch", "runtime_type"} {
				if value, ok := args[key]; ok {
					w.content[key] = value
				}
			}
			if value, ok := args["managed_runtime_config"]; ok {
				w.content["runtime_config"] = value
			}
			if value, ok := args["expected_updated_at"]; ok {
				w.content["expected_updated_at"] = value
			}
		}
	case "bahia_create_environment":
		id, e := mcpIntentEntityID(args, intentID)
		if e != nil {
			return w, e
		}
		w = intentWrite{domain: "environment", op: "create", coordinate: id.String(), family: nostrpool.KindEnvironmentRegistry, stateKey: "id", stateValue: id.String(), content: map[string]any{"id": id.String()}}
		w.orgID, err = s.intentOrgFromState(ctx, args, 0, "", "")
		if err != nil {
			return w, err
		}
		for _, key := range []string{"name", "loom_worker_selector", "runtime_config", "reconcile_mode", "protected", "deploy_strategy"} {
			if value, ok := args[key]; ok {
				w.content[key] = value
			}
		}
		w.content["org_id"] = w.orgID.String()
	case "bahia_update_environment", "bahia_delete_environment":
		id, e := parseRequiredUUIDArg(args, "environment_id")
		if e != nil {
			return w, e
		}
		w = intentWrite{domain: "environment", coordinate: id.String(), family: nostrpool.KindEnvironmentRegistry, stateKey: "id", stateValue: id.String()}
		w.orgID, err = s.intentOrgFromState(ctx, args, w.family, "id", id.String())
		if err != nil {
			return w, err
		}
		if name == "bahia_delete_environment" {
			w.op, w.deleted, w.content = "delete", true, map[string]any{"id": id.String(), "force": boolArg(args, "force")}
		} else {
			w.op = "update"
			w.content, err = s.existingIntentState(ctx, w.family, id.String())
			if err != nil {
				return w, err
			}
			// A partial environment edit does not replace deployment units.
			// The canonical read model includes that field, but copying it
			// would turn an ordinary edit into a complete-set CAS mutation.
			if _, explicit := args["deployment_units"]; !explicit {
				delete(w.content, "deployment_units")
			}
			for _, key := range []string{"name", "loom_worker_selector", "runtime_config", "reconcile_mode", "protected", "deploy_strategy", "expected_updated_at"} {
				if value, ok := args[key]; ok {
					w.content[key] = value
				}
			}
		}
	case "bahia_deploy", "bahia_create_intent", "bahia_rollback", "bahia_assistant_service_deploy", "bahia_assistant_service_rollback":
		serviceID, e := parseRequiredUUIDArg(args, "service_id")
		if e != nil {
			return w, e
		}
		envID, e := parseRequiredUUIDArg(args, "environment_id")
		if e != nil {
			return w, e
		}
		w = intentWrite{domain: "deployment", op: "create", coordinate: serviceID.String() + ":" + envID.String(), family: nostrpool.KindDeploymentIntentRegistry, stateKey: "metadata.nostr_event_id", content: map[string]any{"service_id": serviceID.String(), "environment_id": envID.String()}}
		w.orgID, err = s.intentOrgFromState(ctx, args, nostrpool.KindServiceRegistry, "id", serviceID.String())
		if err != nil {
			return w, err
		}
		if name == "bahia_rollback" || name == "bahia_assistant_service_rollback" {
			w.op = "rollback"
			for _, key := range []string{"target_artifact_id", "target_run_id", "supersedes_intent_id", "expected_updated_at"} {
				if value, ok := args[key]; ok {
					w.content[key] = value
				}
			}
			if e := s.resolveRollbackDefaults(ctx, serviceID, envID, w.content); e != nil {
				return w, e
			}
		} else {
			artifactID, e := parseRequiredUUIDArg(args, "artifact_id")
			if e != nil {
				return w, e
			}
			w.content["artifact_id"] = artifactID.String()
		}
	case "bahia_approve_deployment", "bahia_approve_intent", "bahia_reject_deployment", "bahia_reject_intent":
		target, e := parseRequiredUUIDArg(args, "intent_id")
		if e != nil {
			return w, e
		}
		w = intentWrite{domain: "deployment", coordinate: target.String(), family: nostrpool.KindDeploymentIntentRegistry, stateKey: "id", stateValue: target.String(), content: map[string]any{"deployment_intent_id": target.String()}}
		w.orgID, err = s.intentOrgFromState(ctx, args, w.family, "id", target.String())
		if err != nil {
			return w, err
		}
		if name == "bahia_approve_deployment" || name == "bahia_approve_intent" {
			w.op = "approve"
		} else {
			w.op = "reject"
		}
		if value, ok := args["expected_updated_at"]; ok {
			w.content["expected_updated_at"] = value
		}
	case "bahia_create_policy":
		id, e := mcpIntentEntityID(args, intentID)
		if e != nil {
			return w, e
		}
		w = intentWrite{domain: "policy", op: "create", coordinate: id.String(), family: nostrpool.KindPolicyRegistry, stateKey: "id", stateValue: id.String(), content: map[string]any{"id": id.String()}}
		w.orgID, err = s.intentOrgFromState(ctx, args, nostrpool.KindEnvironmentRegistry, "id", stringArg(args, "environment_id"))
		if err != nil {
			return w, err
		}
		for _, key := range []string{"name", "environment_id", "rules", "enforcement", "enabled"} {
			if value, ok := args[key]; ok {
				w.content[key] = value
			}
		}
	case "bahia_update_policy", "bahia_delete_policy":
		id, e := parseRequiredUUIDArg(args, "policy_id")
		if e != nil {
			return w, e
		}
		w = intentWrite{domain: "policy", coordinate: id.String(), family: nostrpool.KindPolicyRegistry, stateKey: "id", stateValue: id.String()}
		w.orgID, err = s.intentOrgFromState(ctx, args, w.family, "id", id.String())
		if err != nil {
			return w, err
		}
		if name == "bahia_delete_policy" {
			w.op, w.deleted, w.content = "delete", true, map[string]any{"id": id.String()}
		} else {
			w.op = "update"
			w.content, err = s.existingIntentState(ctx, w.family, id.String())
			if err != nil {
				return w, err
			}
			for _, key := range []string{"name", "environment_id", "rules", "enforcement", "enabled", "expected_updated_at"} {
				if value, ok := args[key]; ok {
					w.content[key] = value
				}
			}
		}
	case "bahia_llm_create_route":
		id, e := mcpIntentEntityID(args, intentID)
		if e != nil {
			return w, e
		}
		w = intentWrite{domain: "llm", op: "create", coordinate: id.String(), family: nostrpool.KindLLMRouteRegistry, stateKey: "id", stateValue: id.String(), content: map[string]any{"id": id.String()}}
		w.orgID, err = s.intentOrgFromState(ctx, args, 0, "", "")
		if err != nil {
			return w, err
		}
		for _, key := range []string{"name", "description", "gateway_config", "default_placement_policy", "default_promotion_gate", "metadata"} {
			if value, ok := args[key]; ok {
				w.content[key] = value
			}
		}
	case "bahia_llm_update_route":
		id, e := parseRequiredUUIDArg(args, "route_id")
		if e != nil {
			return w, e
		}
		w = intentWrite{domain: "llm", op: "update", coordinate: id.String(), family: nostrpool.KindLLMRouteRegistry, stateKey: "id", stateValue: id.String()}
		w.orgID, err = s.intentOrgFromState(ctx, args, 0, "", "")
		if err != nil {
			return w, err
		}
		w.content, err = s.existingIntentState(ctx, w.family, id.String())
		if err != nil {
			return w, err
		}
		for _, key := range []string{"name", "description", "gateway_config", "default_placement_policy", "default_promotion_gate", "metadata", "expected_updated_at"} {
			if value, ok := args[key]; ok {
				w.content[key] = value
			}
		}
	case "bahia_llm_register_release":
		id, e := mcpIntentEntityID(args, intentID)
		if e != nil {
			return w, e
		}
		routeID, e := parseRequiredUUIDArg(args, "route_id")
		if e != nil {
			return w, e
		}
		w = intentWrite{domain: "llm", op: "release-register", coordinate: "llm-release:" + id.String(), family: nostrpool.KindLLMRouteRegistry, stateKey: "id", stateValue: routeID.String(), content: map[string]any{"id": id.String(), "route_id": routeID.String()}}
		w.orgID, err = s.intentOrgFromState(ctx, args, 0, "", "")
		if err != nil {
			return w, err
		}
		for _, key := range []string{"version", "model_ref", "model_source", "model_revision", "estimated_vram_gb", "backend_preferences", "runtime_backend", "external_backend", "placement_policy", "promotion_gate", "metadata"} {
			if value, ok := args[key]; ok {
				w.content[key] = value
			}
		}
	case "bahia_llm_deploy", "bahia_assistant_llm_deploy", "bahia_llm_rollback", "bahia_assistant_llm_rollback":
		routeID, e := parseRequiredUUIDArg(args, "route_id")
		if e != nil {
			return w, e
		}
		envID, e := parseRequiredUUIDArg(args, "environment_id")
		if e != nil {
			return w, e
		}
		w = intentWrite{domain: "llm", op: "deploy", coordinate: routeID.String() + ":" + envID.String(), content: map[string]any{"route_id": routeID.String(), "environment_id": envID.String()}}
		w.orgID, err = s.intentOrgFromState(ctx, args, nostrpool.KindEnvironmentRegistry, "id", envID.String())
		if err != nil {
			return w, err
		}
		if name == "bahia_llm_rollback" || name == "bahia_assistant_llm_rollback" {
			w.op = "rollback"
		} else {
			releaseID, e := parseRequiredUUIDArg(args, "release_id")
			if e != nil {
				return w, e
			}
			w.content["release_id"] = releaseID.String()
		}
		if value, ok := args["metadata"]; ok {
			w.content["metadata"] = value
		}
	case "bahia_llm_approve_deployment", "bahia_llm_reject_deployment", "bahia_assistant_llm_approve_deployment":
		target, e := parseRequiredUUIDArg(args, "intent_id")
		if e != nil {
			return w, e
		}
		w = intentWrite{domain: "llm", coordinate: target.String(), content: map[string]any{"deployment_intent_id": target.String()}}
		w.orgID, err = s.intentOrgFromState(ctx, args, 0, "", "")
		if err != nil {
			return w, err
		}
		if name == "bahia_llm_reject_deployment" || strings.EqualFold(stringArg(args, "decision"), "reject") {
			w.op = "reject"
		} else {
			w.op = "approve"
		}
		if value, ok := args["expected_updated_at"]; ok {
			w.content["expected_updated_at"] = value
		}
	case "bahia_create_secret":
		id, e := mcpIntentEntityID(args, intentID)
		if e != nil {
			return w, e
		}
		serviceID, e := parseRequiredUUIDArg(args, "service_id")
		if e != nil {
			return w, e
		}
		if strings.TrimSpace(stringArg(args, "name")) == "" || stringArg(args, "value") == "" {
			return w, fmt.Errorf("name and value are required")
		}
		w = intentWrite{domain: "secret", op: "create", coordinate: id.String(), family: nostrpool.KindSecretRegistry, stateKey: "id", stateValue: id.String()}
		w.orgID, err = s.intentOrgFromState(ctx, nil, nostrpool.KindServiceRegistry, "id", serviceID.String())
		if err != nil {
			return w, err
		}
		envID, e := optionalUUIDArgStrict(args, "environment_id")
		if e != nil {
			return w, e
		}
		var envPtr *uuid.UUID
		if envID != uuid.Nil {
			envPtr = &envID
		}
		w.content = controlplane.BuildSecretIntentContent(serviceID, id, stringArg(args, "name"), stringArg(args, "value"), envPtr, domain.EncryptionNIP44)
	case "bahia_update_secret", "bahia_delete_secret":
		id, e := parseRequiredUUIDArg(args, "secret_id")
		if e != nil {
			return w, e
		}
		w = intentWrite{domain: "secret", coordinate: id.String(), family: nostrpool.KindSecretRegistry, stateKey: "id", stateValue: id.String()}
		current, e := s.existingIntentState(ctx, w.family, id.String())
		if e != nil {
			return w, e
		}
		serviceID, e := uuid.Parse(stringFromRecord(current, "service_id"))
		if e != nil || serviceID == uuid.Nil {
			return w, fmt.Errorf("canonical secret has no service_id")
		}
		w.orgID, err = s.intentOrgFromState(ctx, nil, nostrpool.KindServiceRegistry, "id", serviceID.String())
		if err != nil {
			return w, err
		}
		if name == "bahia_delete_secret" {
			w.op, w.deleted, w.content = "delete", true, map[string]any{"id": id.String()}
		} else {
			if stringArg(args, "value") == "" {
				return w, fmt.Errorf("value is required")
			}
			w.op = "update"
			var envPtr *uuid.UUID
			if raw := stringFromRecord(current, "environment_id"); raw != "" {
				envID, e := uuid.Parse(raw)
				if e != nil {
					return w, fmt.Errorf("canonical secret has invalid environment_id")
				}
				envPtr = &envID
			}
			w.content = controlplane.BuildSecretIntentContent(serviceID, id, stringFromRecord(current, "name"), stringArg(args, "value"), envPtr, domain.EncryptionNIP44)
			if value, ok := args["expected_updated_at"]; ok {
				w.content["expected_updated_at"] = value
			}
		}
	case "bahia_create_notification_channel":
		id, e := mcpIntentEntityID(args, intentID)
		if e != nil {
			return w, e
		}
		w = intentWrite{domain: "notification", op: "create", coordinate: id.String(), family: nostrpool.KindNotificationChannelRegistry, stateKey: "id", stateValue: id.String()}
		w.orgID, err = s.intentOrgFromState(ctx, args, 0, "", "")
		if err != nil {
			return w, err
		}
		config, ok := args["config"].(map[string]any)
		if !ok {
			return w, fmt.Errorf("config object is required")
		}
		ch := &domain.NotificationChannel{ID: id, OrgID: w.orgID, Name: stringArg(args, "name"), ChannelType: domain.ChannelType(stringArg(args, "channel_type")), Config: config, Enabled: true}
		if value, ok := args["event_filter"].(map[string]any); ok {
			ch.EventFilter = value
		}
		if value, ok := args["enabled"].(bool); ok {
			ch.Enabled = value
		}
		w.content = controlplane.BuildNotificationIntentContent(ch)
	case "bahia_update_notification_channel", "bahia_delete_notification_channel":
		id, e := parseRequiredUUIDArg(args, "channel_id")
		if e != nil {
			return w, e
		}
		w = intentWrite{domain: "notification", coordinate: id.String(), family: nostrpool.KindNotificationChannelRegistry, stateKey: "id", stateValue: id.String()}
		current, e := s.existingIntentState(ctx, w.family, id.String())
		if e != nil {
			return w, e
		}
		w.orgID, err = s.intentOrgFromState(ctx, nil, w.family, "id", id.String())
		if err != nil {
			return w, err
		}
		if name == "bahia_delete_notification_channel" {
			w.op, w.deleted, w.content = "delete", true, map[string]any{"id": id.String()}
		} else {
			w.op = "update"
			encoded, e := json.Marshal(current)
			if e != nil {
				return w, e
			}
			var ch domain.NotificationChannel
			if e := json.Unmarshal(encoded, &ch); e != nil {
				return w, e
			}
			if value, ok := args["name"].(string); ok {
				ch.Name = value
			}
			if value, ok := args["channel_type"].(string); ok {
				ch.ChannelType = domain.ChannelType(value)
			}
			if value, ok := args["config"].(map[string]any); ok {
				ch.Config = value
			}
			if value, ok := args["event_filter"].(map[string]any); ok {
				ch.EventFilter = value
			}
			if value, ok := args["enabled"].(bool); ok {
				ch.Enabled = value
			}
			w.content = controlplane.BuildNotificationIntentContent(&ch)
			if value, ok := args["expected_updated_at"]; ok {
				w.content["expected_updated_at"] = value
			}
		}
	case "bahia_worker_cordon", "bahia_worker_uncordon", "bahia_worker_drain", "bahia_worker_undrain", "bahia_worker_maintenance_enter", "bahia_worker_maintenance_exit", "bahia_worker_labels_update":
		pubkey := strings.ToLower(strings.TrimSpace(stringArg(args, "worker_pubkey")))
		if _, e := nostr.PubKeyFromHex(pubkey); e != nil {
			return w, fmt.Errorf("worker_pubkey must be a 64-character Nostr pubkey")
		}
		current, e := s.readStateOne(ctx, nostrpool.KindWorkerState, "pubkey", pubkey)
		if e != nil {
			return w, e
		}
		if current == nil {
			return w, fmt.Errorf("canonical worker %s not found", pubkey)
		}
		op := strings.TrimPrefix(name, "bahia_worker_")
		op = strings.ReplaceAll(op, "_", "-")
		scheduling := stringFromRecord(current.Fields, "scheduling_state")
		switch op {
		case "cordon":
			scheduling = "cordoned"
		case "drain":
			scheduling = "draining"
		case "maintenance-enter":
			scheduling = "maintenance"
		case "uncordon", "undrain", "maintenance-exit":
			scheduling = "active"
		}
		labels, _ := current.Fields["labels"].(map[string]any)
		if labels == nil {
			labels = map[string]any{}
		}
		if op == "labels-update" {
			provided, ok := args["labels"].(map[string]any)
			if !ok {
				return w, fmt.Errorf("labels object is required")
			}
			labels = provided
		}
		w = intentWrite{domain: "worker", op: op, coordinate: "worker:" + pubkey, family: nostrpool.KindWorkerState, stateKey: "pubkey", stateValue: pubkey, content: map[string]any{"worker_pubkey": pubkey, "scheduling_state": scheduling, "labels": labels}}
		if reason := strings.TrimSpace(stringArg(args, "reason")); reason != "" {
			w.content["reason"] = reason
		}
		if revision, ok := args["expected_updated_at"]; ok {
			w.content["expected_updated_at"] = revision
		}
	default:
		return w, fmt.Errorf("unsupported intent write tool %s", name)
	}
	return w, nil
}

func (s *Server) backupIntentWrite(ctx context.Context, name string, args map[string]any, intentID string) (intentWrite, error) {
	w := intentWrite{domain: "backup", content: map[string]any{}}
	var err error
	w.orgID, err = s.intentOrgFromState(ctx, args, 0, "", "")
	if err != nil {
		return w, err
	}
	id, err := uuid.Parse(intentID)
	if err != nil {
		return w, err
	}
	switch name {
	case "apply_backup_repository":
		repo, e := backupRepositoryFromArgs(args)
		if e != nil {
			return w, e
		}
		if repo.ID == uuid.Nil {
			repo.ID = id
		}
		w.op, w.coordinate, w.family, w.stateKey, w.stateValue = "repository-register", "backup-repository:"+repo.ID.String(), nostrpool.KindBackupRepositoryRegistry, "id", repo.ID.String()
		w.content, err = typedIntentContent(repo)
	case "apply_backup_policy":
		policy, e := backupPolicyFromArgs(args)
		if e != nil {
			return w, e
		}
		if policy.ID == uuid.Nil {
			policy.ID = id
		}
		w.op, w.coordinate, w.family, w.stateKey, w.stateValue = "policy-apply", "backup-policy:"+policy.ID.String(), nostrpool.KindBackupPolicyRegistry, "id", policy.ID.String()
		w.content, err = typedIntentContent(policy)
	case "apply_backup_recipe":
		recipe, e := backupRecipeFromArgs(args)
		if e != nil {
			return w, e
		}
		if recipe.ID == uuid.Nil {
			recipe.ID = id
		}
		w.op, w.coordinate, w.family, w.stateKey, w.stateValue = "recipe-apply", "backup-recipe:"+recipe.ID.String(), nostrpool.KindBackupRecipeRegistry, "id", recipe.ID.String()
		w.content, err = typedIntentContent(recipe)
	case "apply_backup_definition":
		definition, e := backupDefinitionFromArgs(args)
		if e != nil {
			return w, e
		}
		if definition.ID == uuid.Nil {
			definition.ID = id
		}
		w.op, w.coordinate, w.family, w.stateKey, w.stateValue = "definition-apply", "backup-definition:"+definition.ID.String(), nostrpool.KindBackupDefinitionRegistry, "id", definition.ID.String()
		w.content, err = typedIntentContent(definition)
	case "probe_backup_repository":
		w.op, w.coordinate = "repository-probe", "backup-repository-probe:"+intentID
		for _, key := range []string{"repository_id", "repository", "name", "metadata"} {
			if value, ok := args[key]; ok {
				w.content[key] = value
			}
		}
		if w.content["repository"] == nil {
			w.content["repository"] = w.content["name"]
		}
	case "request_backup_run":
		recipeID, e := optionalUUIDArgStrict(args, "recipe_id")
		if e != nil {
			return w, e
		}
		if recipeID == uuid.Nil {
			recipeName := firstNonEmpty(stringArg(args, "recipe"), stringArg(args, "name"))
			if recipeName == "" {
				return w, fmt.Errorf("recipe_id or recipe is required")
			}
			record, e := s.readStateOne(ctx, nostrpool.KindBackupRecipeRegistry, "name", recipeName)
			if e != nil {
				return w, e
			}
			if record == nil {
				return w, fmt.Errorf("canonical backup recipe %q not found", recipeName)
			}
			recipeID, e = uuid.Parse(stringFromRecord(record.Fields, "id"))
			if e != nil {
				return w, e
			}
		}
		run := domain.BackupRun{ID: id, RecipeID: recipeID, Metadata: anyMapFromArg(args["metadata"])}
		w.op, w.coordinate, w.family, w.stateKey, w.stateValue = "run", "backup-run:"+id.String(), nostrpool.KindBackupRunState, "id", id.String()
		w.content, err = typedIntentContent(run)
	case "request_backup_verification":
		runID, e := parseRequiredUUIDArg(args, "backup_run_id")
		if e != nil {
			return w, e
		}
		verification := domain.BackupVerificationRecord{ID: id, BackupRunID: runID, Mode: domain.BackupVerificationMode(stringArg(args, "mode"))}
		w.op, w.coordinate, w.family, w.stateKey, w.stateValue = "verification", "backup-verification:"+id.String(), nostrpool.KindBackupVerificationState, "id", id.String()
		w.content, err = typedIntentContent(verification)
	case "request_backup_restore":
		runID, e := parseRequiredUUIDArg(args, "backup_run_id")
		if e != nil {
			return w, e
		}
		restore := domain.BackupRestoreRun{ID: id, BackupRunID: runID, RestoreTargetRef: stringArg(args, "restore_target_ref"), Metadata: anyMapFromArg(args["metadata"])}
		w.op, w.coordinate, w.family, w.stateKey, w.stateValue = "restore", "backup-restore:"+id.String(), nostrpool.KindBackupRestoreState, "id", id.String()
		w.content, err = typedIntentContent(restore)
	case "approve_backup_restore", "reject_backup_restore":
		restoreID, e := parseRequiredUUIDArg(args, "restore_id")
		if e != nil {
			return w, e
		}
		w.op, w.coordinate, w.family, w.stateKey, w.stateValue = "restore-approval", "backup-restore:"+restoreID.String(), nostrpool.KindBackupRestoreState, "id", restoreID.String()
		w.content = map[string]any{"restore_id": restoreID.String(), "approved": name == "approve_backup_restore", "message": stringArg(args, "message"), "reason_code": stringArg(args, "reason_code")}
		if reason, ok := args["reason"].(map[string]any); ok {
			w.content["reason"] = reason
		}
		if value, ok := args["expected_updated_at"]; ok {
			w.content["expected_updated_at"] = value
		}
	case "request_backup_retention":
		repositoryID, e := parseRequiredUUIDArg(args, "repository_id")
		if e != nil {
			return w, e
		}
		policyID, e := parseRequiredUUIDArg(args, "policy_id")
		if e != nil {
			return w, e
		}
		run := domain.BackupRetentionRun{ID: id, RepositoryID: repositoryID, PolicyID: &policyID, DryRun: boolArg(args, "dry_run"), Metadata: anyMapFromArg(args["metadata"])}
		w.op, w.coordinate, w.family, w.stateKey, w.stateValue = "retention", "backup-retention:"+id.String(), nostrpool.KindBackupRetentionRegistry, "id", id.String()
		w.content, err = typedIntentContent(run)
	default:
		return w, fmt.Errorf("unsupported backup tool %s", name)
	}
	return w, err
}

func describeIntentWriteTools(tools []Tool) []Tool {
	for i := range tools {
		if !isIntentWriteTool(tools[i].Name) || strings.HasPrefix(tools[i].Name, "bahia_assistant_") {
			continue
		}
		tools[i].Description = "Apply a kind-30900 intent in-process; returns canonical state when visible or pending correlation"
		properties, ok := tools[i].InputSchema["properties"].(map[string]interface{})
		if !ok {
			continue
		}
		properties["org_id"] = map[string]interface{}{"type": "string", "description": "Owning organization UUID; required when not derivable from canonical state"}
		properties["idempotency_key"] = map[string]interface{}{"type": "string", "description": "Stable key for retry deduplication; MCP _meta.progressToken is used when omitted"}
		properties["expected_updated_at"] = map[string]interface{}{"type": "string", "description": "Optional RFC3339 revision token for updates"}
		if tools[i].Name == "bahia_rollback" || tools[i].Name == "bahia_assistant_service_rollback" {
			properties["target_artifact_id"] = map[string]interface{}{"type": "string", "description": "Rollback artifact UUID; defaults to latest previously deployed artifact"}
			properties["target_run_id"] = map[string]interface{}{"type": "string", "description": "Successful target run UUID as an alternative to target_artifact_id"}
			properties["supersedes_intent_id"] = map[string]interface{}{"type": "string", "description": "Current deployment intent UUID; defaults to latest canonical intent"}
		}
	}
	return tools
}
