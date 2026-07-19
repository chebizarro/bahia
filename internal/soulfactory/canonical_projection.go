package soulfactory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	cascadia "git.sharegap.net/cascadia/cascadia-go"
)

const (
	canonicalSoulFactoryDomain            = "soul-factory"
	canonicalProvisioningEntity           = "provisioning"
	canonicalProvisioningStateSchema      = "bahia.state.soul-factory-provisioning.v1"
	canonicalSoulFactoryAuditSchema       = "bahia.audit.v1"
	canonicalProvisioningCoordinatePrefix = "soul-factory:provisioning:"
)

func (r *Reactor) publishCanonicalProvisioningObservable(ctx context.Context, requestEvent, resultEvent *nostr.Event) error {
	if requestEvent == nil || resultEvent == nil || tagValue(requestEvent.Tags, "method") != ContextVMMethodProvision {
		return nil
	}
	requestID := requestEvent.ID.Hex()
	status := strings.TrimSpace(tagValue(resultEvent.Tags, tagStatus))
	if status == "" {
		return fmt.Errorf("canonical Soul Factory provisioning projection requires status")
	}
	now := time.Now().UTC()
	dTag := canonicalProvisioningCoordinatePrefix + requestID
	stateBody := map[string]any{
		"schema":           canonicalProvisioningStateSchema,
		"deleted":          false,
		"request_event_id": requestID,
		"requester_pubkey": requestEvent.PubKey.Hex(),
		"status":           status,
		"result_event_id":  resultEvent.ID.Hex(),
		"result_kind":      int(resultEvent.Kind),
		"updated_at":       now.Format(time.RFC3339Nano),
	}
	if soul := tagValue(resultEvent.Tags, tagSoul); soul != "" {
		stateBody["soul"] = soul
	}
	if step := tagValue(resultEvent.Tags, tagStep); step != "" {
		stateBody["step"] = step
	}
	if status == "error" {
		stateBody["error"] = resultEvent.Content
	}
	stateContent, err := json.Marshal(stateBody)
	if err != nil {
		return fmt.Errorf("marshal canonical Soul Factory provisioning state: %w", err)
	}
	state := &nostr.Event{
		Kind:      nostr.Kind(cascadia.CAS_CP_STATE),
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", dTag},
			{"domain", canonicalSoulFactoryDomain},
			{"entity", canonicalProvisioningEntity},
			{"schema", canonicalProvisioningStateSchema},
			{"request", requestID},
			{"status", status},
			{"deleted", "false"},
			{tagEvent, resultEvent.ID.Hex(), "soul-factory-result"},
			{tagPubkey, requestEvent.PubKey.Hex(), "requester"},
		},
		Content: string(stateContent),
	}
	if err := r.signer.Sign(ctx, state); err != nil {
		return fmt.Errorf("sign canonical Soul Factory provisioning state: %w", err)
	}
	if err := r.publish(ctx, state, r.provisioningPublicationRelays()); err != nil {
		return fmt.Errorf("publish canonical Soul Factory provisioning state: %w", err)
	}

	auditType := "soul-factory.provisioning." + status
	auditBody := map[string]any{
		"schema":           canonicalSoulFactoryAuditSchema,
		"type":             auditType,
		"domain":           canonicalSoulFactoryDomain,
		"entity":           canonicalProvisioningEntity,
		"request_event_id": requestID,
		"requester_pubkey": requestEvent.PubKey.Hex(),
		"result_event_id":  resultEvent.ID.Hex(),
		"status":           status,
		"state_d_tag":      dTag,
		"recorded_at":      now.Format(time.RFC3339Nano),
	}
	auditContent, err := json.Marshal(auditBody)
	if err != nil {
		return fmt.Errorf("marshal canonical Soul Factory provisioning audit: %w", err)
	}
	audit := &nostr.Event{
		Kind:      nostr.Kind(cascadia.CAS_AUDIT),
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"domain", canonicalSoulFactoryDomain},
			{"entity", canonicalProvisioningEntity},
			{"schema", canonicalSoulFactoryAuditSchema},
			{"type", auditType},
			{"request", requestID},
			{"state", dTag},
			{tagEvent, resultEvent.ID.Hex(), "soul-factory-result"},
			{tagPubkey, requestEvent.PubKey.Hex(), "requester"},
		},
		Content: string(auditContent),
	}
	if err := r.signer.Sign(ctx, audit); err != nil {
		return fmt.Errorf("sign canonical Soul Factory provisioning audit: %w", err)
	}
	if err := r.publish(ctx, audit, r.provisioningPublicationRelays()); err != nil {
		return fmt.Errorf("publish canonical Soul Factory provisioning audit: %w", err)
	}
	return nil
}
