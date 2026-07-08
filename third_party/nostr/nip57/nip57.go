package nip57

import (
	"encoding/json"
	"strconv"

	"fiatjaf.com/nostr"
)

// GetAmountFromZap takes a zap receipt event (kind 9735) and returns the amount in millisats.
// It first checks the event's "amount" tag, and if not present, checks the embedded zap request's "amount" tag.
func GetAmountFromZap(event nostr.Event) uint64 {
	// Check for "amount" tag in the zap receipt
	if tag := event.Tags.Find("amount"); tag != nil {
		if amt, err := strconv.ParseUint(tag[1], 10, 64); err == nil {
			return amt
		}
	}

	// if not found, get from the embedded zap request in "description" tag
	if descTag := event.Tags.Find("description"); descTag != nil {
		var embeddedEvent nostr.Event
		if err := json.Unmarshal([]byte(descTag[1]), &embeddedEvent); err == nil {
			if tag := embeddedEvent.Tags.Find("amount"); tag != nil {
				if amt, err := strconv.ParseUint(tag[1], 10, 64); err == nil {
					return amt
				}
			}
		}
	}

	return 0
}
