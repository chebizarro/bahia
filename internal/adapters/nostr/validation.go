package nostr

import (
	"encoding/hex"
	"fmt"
	"time"

	gonostr "github.com/nbd-wtf/go-nostr"
)

const (
	InboundEventMaxFutureSkew = 10 * time.Minute
	InboundEventMaxPastAge    = 365 * 24 * time.Hour
)

// ValidateInboundEvent verifies the NIP-01 trust boundary for relay-provided events.
// Callers must run this before persistence, deduplication, or handler dispatch.
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

	if ev.Kind < 0 {
		return fmt.Errorf("invalid kind %d", ev.Kind)
	}
	if err := validateHexField("id", ev.ID, 64); err != nil {
		return err
	}
	if err := validateHexField("pubkey", ev.PubKey, 64); err != nil {
		return err
	}
	if err := validateHexField("signature", ev.Sig, 128); err != nil {
		return err
	}
	if ev.CreatedAt <= 0 {
		return fmt.Errorf("created_at is required")
	}
	createdAt := ev.CreatedAt.Time()
	if createdAt.After(now.Add(maxFutureSkew)) {
		return fmt.Errorf("created_at too far in future")
	}
	if createdAt.Before(now.Add(-InboundEventMaxPastAge)) {
		return fmt.Errorf("created_at too far in past")
	}
	if err := validateTags(ev.Tags); err != nil {
		return err
	}
	if !ev.CheckID() {
		return fmt.Errorf("event id does not match serialized event")
	}
	ok, err := ev.CheckSignature()
	if err != nil {
		return fmt.Errorf("invalid signature: %w", err)
	}
	if !ok {
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
