package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"reflect"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// CanonicalWorkflowEvents is the relay-hydrated local signed-event store, not
// the optional PostgreSQL nostr_events audit index.
type CanonicalWorkflowEvents interface {
	QueryEvents(nostr.Filter) iter.Seq[nostr.Event]
}

func retainedSignedRequest(source CanonicalWorkflowEvents, eventID string) (nostr.Event, error) {
	if source == nil {
		return nostr.Event{}, fmt.Errorf("canonical workflow request store is unavailable")
	}
	id, err := nostr.IDFromHex(strings.TrimSpace(eventID))
	if err != nil {
		return nostr.Event{}, fmt.Errorf("invalid request event id: %w", err)
	}
	for event := range source.QueryEvents(nostr.Filter{IDs: []nostr.ID{id}}) {
		if event.ID == id && event.CheckID() && event.VerifySignature() {
			return event, nil
		}
	}
	return nostr.Event{}, fmt.Errorf("validated signed request %s is not retained locally", eventID)
}

func retainedWorkflowReceipt(source CanonicalWorkflowEvents, servicePubkey string, filter nostr.Filter, matches func(nostr.Event) bool) error {
	if source == nil {
		return fmt.Errorf("canonical workflow receipt store is unavailable")
	}
	servicePubkey = strings.ToLower(strings.TrimSpace(servicePubkey))
	if _, err := nostr.PubKeyFromHex(servicePubkey); err != nil {
		return fmt.Errorf("service signing identity is unavailable: %w", err)
	}
	for event := range source.QueryEvents(filter) {
		if event.PubKey.Hex() == servicePubkey && event.CheckID() && event.VerifySignature() && matches(event) {
			return nil
		}
	}
	return fmt.Errorf("matching service-signed workflow acceptance record is not retained locally")
}

func verifyToolApprovalSource(source CanonicalWorkflowEvents, servicePubkey string, intent *domain.ToolProvisionIntent, authorized func(string) bool) error {
	if intent == nil {
		return fmt.Errorf("tool provisioning intent is missing")
	}
	request, err := retainedSignedRequest(source, intent.NostrEventID)
	if err != nil {
		return err
	}
	if request.Kind != KindToolProvisionRequest || request.PubKey.Hex() != strings.ToLower(strings.TrimSpace(intent.RequesterPubkey)) || !authorized(request.PubKey.Hex()) {
		return fmt.Errorf("tool request signer or kind does not match an authorized original request")
	}
	var payload struct {
		ServiceID     string               `json:"service_id"`
		EnvironmentID string               `json:"environment_id"`
		Tools         []domain.ToolRequest `json:"tools"`
	}
	if err := json.Unmarshal([]byte(request.Content), &payload); err != nil {
		return fmt.Errorf("decode retained tool request: %w", err)
	}
	if payload.ServiceID != intent.ServiceID.String() || payload.EnvironmentID != intent.EnvironmentID.String() || !reflect.DeepEqual(payload.Tools, intent.RequestedTools) {
		return fmt.Errorf("tool row does not match retained signed request")
	}
	return retainedWorkflowReceipt(source, servicePubkey, nostr.Filter{
		Kinds: []nostr.Kind{KindCASControlState},
		Tags:  nostr.TagMap{"d": {"tool-provisioning:" + intent.ID.String()}},
	}, func(receipt nostr.Event) bool {
		if tagValueNostr(receipt.Tags, "e") != request.ID.Hex() || tagValueNostr(receipt.Tags, "intent") != intent.ID.String() {
			return false
		}
		var body struct {
			IntentID      string `json:"intent_id"`
			ServiceID     string `json:"service_id"`
			EnvironmentID string `json:"environment_id"`
			Step          string `json:"step"`
		}
		return json.Unmarshal([]byte(receipt.Content), &body) == nil && body.IntentID == intent.ID.String() && body.ServiceID == intent.ServiceID.String() && body.EnvironmentID == intent.EnvironmentID.String() && body.Step == "queued" && receipt.CreatedAt >= request.CreatedAt
	})
}

func verifyBackupRestoreApprovalSource(source CanonicalWorkflowEvents, servicePubkey string, restore *domain.BackupRestoreRun, authorized func(string) bool) error {
	if restore == nil {
		return fmt.Errorf("backup restore is missing")
	}
	request, err := retainedSignedRequest(source, restore.RequestEventID)
	if err != nil {
		return err
	}
	if int(request.Kind) != restore.RequestKind || tagValueNostr(request.Tags, "d") != restore.RequestDTag {
		return fmt.Errorf("backup restore row does not match signed request kind or coordinate")
	}
	switch request.Kind {
	case KindBackupRestoreRequest:
		actor := backupRequestActor(&request)
		parsed, err := parseBackupRestoreRequest(&request)
		if err != nil || actor == "" || authorized == nil || !authorized(actor) || actor != restore.RequestedBy || parsed.BackupRunID != restore.BackupRunID.String() || parsed.RestoreTargetRef != restore.RestoreTargetRef {
			return fmt.Errorf("backup restore row does not match retained signed request")
		}
	case KindCASControlState:
		parsed, err := ParseIntent(&request)
		if err != nil || parsed.Domain != "backup" || parsed.Op != "restore" || authorized == nil || !authorized(request.PubKey.Hex()) || request.PubKey.Hex() != restore.RequestedBy {
			return fmt.Errorf("backup restore row does not match retained signed intent")
		}
		requested, err := backupRestoreFromIntentContent(parsed)
		if err != nil || requested.BackupRunID != restore.BackupRunID || requested.RestoreTargetRef != restore.RestoreTargetRef || (requested.ID != uuid.Nil && requested.ID != restore.ID) {
			return fmt.Errorf("backup restore row differs from signed intent content")
		}
		if requested.ID == restore.ID {
			return nil // the author-signed intent itself binds this restore id
		}
	default:
		return fmt.Errorf("unsupported backup restore request kind %d", request.Kind)
	}
	return retainedWorkflowReceipt(source, servicePubkey, nostr.Filter{
		Kinds: []nostr.Kind{KindBackupRestoreStatus},
		Tags:  nostr.TagMap{"e": {request.ID.Hex()}},
	}, func(receipt nostr.Event) bool {
		if tagValueNostr(receipt.Tags, "restore_id") != restore.ID.String() || tagValueNostr(receipt.Tags, "step") != "pending_approval" {
			return false
		}
		var body struct {
			RequestEventID   string `json:"request_event_id"`
			RestoreID        string `json:"restore_id"`
			BackupRunID      string `json:"backup_run_id"`
			RestoreTargetRef string `json:"restore_target_ref"`
		}
		return json.Unmarshal([]byte(receipt.Content), &body) == nil && body.RequestEventID == request.ID.Hex() && body.RestoreID == restore.ID.String() && body.BackupRunID == restore.BackupRunID.String() && body.RestoreTargetRef == restore.RestoreTargetRef && receipt.CreatedAt >= request.CreatedAt
	})
}

func (r *Reactor) workflowServicePubkey(ctx context.Context) (string, error) {
	if r == nil || r.signer == nil {
		return "", fmt.Errorf("workflow service signer is unavailable")
	}
	pubkey, err := r.signer.GetPublicKey(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve workflow service signer: %w", err)
	}
	return pubkey.Hex(), nil
}
