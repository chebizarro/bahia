package test

import (
	"fiatjaf.com/nostr"
)

func getTimestamps(events []*nostr.Event) []nostr.Timestamp {
	res := make([]nostr.Timestamp, len(events))
	for i, evt := range events {
		res[i] = evt.CreatedAt
	}
	return res
}
