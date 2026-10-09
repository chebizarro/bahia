package controlplane

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
)

const backupRunRequestValidity = 15 * time.Minute

// ValidateSignedBackupRunRequest checks a complete operator-authored run
// request before it enters the intent processor. It does not establish relay
// acceptance or authorize execution; those require canonical receipts and a
// recoverable executor commit protocol.
func ValidateSignedBackupRunRequest(event *nostr.Event, actor string) (*Intent, error) {
	return validateSignedBackupRunRequest(event, actor, time.Now().UTC())
}

func validateSignedBackupRunRequest(event *nostr.Event, actor string, now time.Time) (*Intent, error) {
	if err := nostradapter.ValidateInboundEvent(event, now, nostradapter.InboundEventMaxFutureSkew); err != nil {
		return nil, fmt.Errorf("invalid signed backup request: %w", err)
	}
	created := event.CreatedAt.Time()
	if now.Sub(created) > backupRunRequestValidity {
		return nil, fmt.Errorf("signed backup request is older than the 15-minute intake window")
	}
	var expires time.Time
	seenExpiration := false
	for _, tag := range event.Tags {
		if len(tag) == 0 || tag[0] != "expiration" {
			continue
		}
		if seenExpiration || len(tag) != 2 {
			return nil, fmt.Errorf("signed backup request requires exactly one NIP-40 expiration tag")
		}
		seenExpiration = true
		seconds, err := strconv.ParseInt(tag[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("signed backup request has an invalid NIP-40 expiration: %w", err)
		}
		expires = time.Unix(seconds, 0).UTC()
	}
	if !seenExpiration || !expires.After(created) || expires.After(created.Add(backupRunRequestValidity)) || !now.Before(expires) {
		return nil, fmt.Errorf("signed backup request needs an unexpired NIP-40 expiration within 15 minutes of creation")
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
