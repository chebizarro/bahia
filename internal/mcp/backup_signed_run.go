package mcp

import (
	"context"
	"encoding/json"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/controlplane"
)

// callSignedBackupRun only hands a signed operator event already present in
// the local event store to the intent processor. Local observation is not a
// relay OK/ACK or delivery quorum; MCP cannot infer canonical acceptance from
// this check. Admission remains pending until the service-signed run state is
// delivered, and backup execution remains disabled.
func (s *Server) callSignedBackupRun(ctx context.Context, args map[string]interface{}) *ToolResult {
	principal := auth.GetPrincipal(ctx)
	if principal == nil || !principal.IsAuthenticated() {
		return intentWriteError("rejected", "", "", "authentication required")
	}
	actor := strings.ToLower(strings.TrimSpace(principal.PubKey))
	if _, err := nostr.PubKeyFromHex(actor); err != nil {
		return intentWriteError("rejected", "", "", "authenticated Nostr pubkey required")
	}
	raw, ok := args["signed_intent_event"]
	if !ok || raw == nil {
		return intentWriteError("rejected", "", "", "operator-signed intent event required; unsigned MCP backup run requests are refused")
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return intentWriteError("rejected", "", "", "invalid signed intent event")
	}
	var event nostr.Event
	if err := json.Unmarshal(encoded, &event); err != nil {
		return intentWriteError("rejected", "", "", "invalid signed intent event")
	}
	intent, err := controlplane.ValidateSignedBackupRunRequest(&event, actor)
	if err != nil {
		return intentWriteError("rejected", "", event.ID.Hex(), err.Error())
	}
	if s.stateStore == nil {
		return intentWriteError("rejected", intent.IntentID, event.ID.Hex(), "local relay subscription is unavailable")
	}
	observed := false
	for stored := range s.stateStore.QueryEvents(nostr.Filter{IDs: []nostr.ID{event.ID}, Kinds: []nostr.Kind{event.Kind}, Authors: []nostr.PubKey{event.PubKey}}) {
		if stored.ID == event.ID && stored.PubKey == event.PubKey && stored.CheckID() && stored.VerifySignature() {
			observed = true
			break
		}
	}
	if !observed {
		return intentWriteError("rejected", intent.IntentID, event.ID.Hex(), "signed request is not present in the local relay-synced event store; publish it to the relay first")
	}
	if prior := s.intentProc.ProcessedIntent(intent.IntentID); prior != nil &&
		(prior.Actor != actor || prior.Domain != "backup" || prior.Op != "run" || prior.Coordinate != intent.Coordinate || prior.EventID != event.ID.Hex()) {
		return intentWriteError("conflict", intent.IntentID, event.ID.Hex(), "idempotency key belongs to another signed backup request")
	}
	if err := s.intentProc.ProcessInProcess(ctx, intent); err != nil {
		return intentWriteError("rejected", intent.IntentID, event.ID.Hex(), err.Error())
	}
	if s.intentProc.ProcessedIntent(intent.IntentID) == nil {
		if _, staged := intent.Result["state_event_id"].(string); !staged {
			return intentWriteError("rejected", intent.IntentID, event.ID.Hex(), "signed request was not staged by the intent processor")
		}
		result, _ := jsonResult(map[string]any{"status": "pending", "intent_id": intent.IntentID, "event_id": event.ID.Hex(), "state_event_id": intent.Result["state_event_id"]})
		return result
	}
	result, _ := jsonResult(map[string]any{"status": "accepted", "intent_id": intent.IntentID, "event_id": event.ID.Hex(), "state_event_id": intent.Result["state_event_id"]})
	return result
}
