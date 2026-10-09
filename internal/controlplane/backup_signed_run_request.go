package controlplane

import (
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
)

// ValidateSignedBackupRunRequest checks a complete operator-authored run
// request before it enters the intent processor. It does not establish relay
// acceptance or authorize execution; those require canonical receipts and a
// recoverable executor commit protocol.
func ValidateSignedBackupRunRequest(event *nostr.Event, actor string) (*Intent, error) {
	if err := nostradapter.ValidateInboundEvent(event, time.Now().UTC(), nostradapter.InboundEventMaxFutureSkew); err != nil {
		return nil, fmt.Errorf("invalid signed backup request: %w", err)
	}
	if event.PubKey.Hex() != actor {
		return nil, fmt.Errorf("signed backup request author differs from authenticated operator")
	}
	intent, err := ParseIntent(event)
	if err != nil {
		return nil, err
	}
	if intent.Domain != "backup" || intent.Op != "run" {
		return nil, fmt.Errorf("signed event is not a backup run intent")
	}
	for key := range intent.Content {
		switch key {
		case "id", "recipe_id", "repository_id", "policy_id", "backend", "target_ref", "verification_mode", "execution_snapshot", "metadata", "intent_id":
		default:
			return nil, fmt.Errorf("backup request contains non-request field %q", key)
		}
	}
	if contentIntentID, ok := intent.Content["intent_id"]; ok && contentIntentID != intent.IntentID {
		return nil, fmt.Errorf("backup request content intent_id differs from signed tag")
	}
	run, err := backupRunFromIntentContent(intent)
	if err != nil || run.ID == uuid.Nil || run.ID.Version() != 7 {
		return nil, fmt.Errorf("backup request requires an author-minted UUIDv7 run id")
	}
	if intent.Coordinate != "backup-run:"+run.ID.String() {
		return nil, fmt.Errorf("backup request coordinate does not bind the run id")
	}
	if run.RecipeID == uuid.Nil || run.RepositoryID == uuid.Nil || !run.Backend.IsValid() ||
		strings.TrimSpace(run.TargetRef) == "" || !run.VerificationMode.IsValid() {
		return nil, fmt.Errorf("backup request omits resolved execution inputs")
	}
	if err := validateBackupExecutionSnapshot(run, run); err != nil {
		return nil, err
	}
	for _, source := range []string{run.ExecutionSnapshot.RecipeEventID, run.ExecutionSnapshot.RepositoryEventID} {
		if _, err := nostr.IDFromHex(source); err != nil {
			return nil, fmt.Errorf("backup request has an invalid canonical config event id: %w", err)
		}
	}
	if run.ExecutionSnapshot.Policy != nil {
		if _, err := nostr.IDFromHex(run.ExecutionSnapshot.PolicyEventID); err != nil {
			return nil, fmt.Errorf("backup request has an invalid canonical policy event id: %w", err)
		}
	}
	intent.Actor = actor
	return intent, nil
}
