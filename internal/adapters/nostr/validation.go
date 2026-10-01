package nostr

import (
	"encoding/hex"
	"fmt"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/nostrutil"
)

const (
	InboundEventMaxFutureSkew = 10 * time.Minute
	// InboundEventMaxPastAge bounds how old an event of a kind
	// nostrutil.AgeCapped reports (regular and ephemeral) may be. It does not
	// apply to replaceable or addressable state or to deletion requests.
	InboundEventMaxPastAge = nostrutil.MaxEventAge
)

// ValidateInboundEvent verifies the NIP-01 trust boundary for relay-provided events.
// Callers must run this before persistence, deduplication, or handler dispatch.
// It also drops events whose NIP-40 expiration has passed, so no consumer
// acts on expired state.
func ValidateInboundEvent(ev *gonostr.Event, now time.Time, maxFutureSkew time.Duration) error {
	if ev == nil {
		return fmt.Errorf("nil event")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if maxFutureSkew <= 0 {
		maxFutureSkew = InboundEventMaxFutureSkew
	}

	if err := validateHexField("id", ev.ID.Hex(), 64); err != nil {
		return err
	}
	if err := validateHexField("pubkey", ev.PubKey.Hex(), 64); err != nil {
		return err
	}
	if err := validateHexField("signature", eventSignatureHex(ev), 128); err != nil {
		return err
	}
	if eventKindInt(ev) < 0 {
		return fmt.Errorf("kind must be non-negative")
	}
	if ev.CreatedAt <= 0 {
		return fmt.Errorf("created_at is required")
	}
	createdAt := ev.CreatedAt.Time()
	if createdAt.After(now.Add(maxFutureSkew)) {
		return fmt.Errorf("created_at too far in future")
	}
	if nostrutil.AgeCapped(ev.Kind) && createdAt.Before(now.Add(-InboundEventMaxPastAge)) {
		return fmt.Errorf("created_at too far in past")
	}
	if nostrutil.Expired(ev, now) {
		return fmt.Errorf("event expired (NIP-40)")
	}
	if err := validateTags(ev.Tags); err != nil {
		return err
	}
	if !ev.CheckID() {
		return fmt.Errorf("event id does not match serialized event")
	}
	if !ev.VerifySignature() {
		return fmt.Errorf("invalid signature")
	}
	return nil
}

func validateHexField(name, value string, expectedLen int) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len(value) != expectedLen {
		return fmt.Errorf("%s must be %d hex characters", name, expectedLen)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("%s must be valid hex: %w", name, err)
	}
	return nil
}

func validateTags(tags gonostr.Tags) error {
	if tags == nil {
		return nil
	}
	for i, tag := range tags {
		if tag == nil {
			return fmt.Errorf("tag %d is nil", i)
		}
		if len(tag) == 0 {
			return fmt.Errorf("tag %d is empty", i)
		}
		for j, value := range tag {
			if value == "" && j == 0 {
				return fmt.Errorf("tag %d has empty key", i)
			}
		}
	}
	return nil
}
