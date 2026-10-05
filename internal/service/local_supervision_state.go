package service

import (
	"encoding/json"
	"fmt"
	"strings"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// LocalSupervisionState reads the daemon's subscribed, verified cp-state
// history. The store is populated by relay catch-up and live subscriptions;
// SQL projections are deliberately not consulted by supervisors.
type LocalSupervisionState struct {
	Store  *localstore.Store
	Author string
}

func (s LocalSupervisionState) records(topic string) ([]gonostr.Event, error) {
	if s.Store == nil {
		return nil, fmt.Errorf("local supervision event store is required")
	}
	key, err := gonostr.PubKeyFromHex(strings.TrimSpace(s.Author))
	if err != nil {
		return nil, fmt.Errorf("local supervision author: %w", err)
	}
	filter := gonostr.Filter{Kinds: []gonostr.Kind{gonostr.Kind(kinds.CASControlState)}, Authors: []gonostr.PubKey{key}, Tags: gonostr.TagMap{"t": {topic}}}
	var records []gonostr.Event
	for event := range s.Store.QueryEvents(filter) {
		records = append(records, event)
	}
	return records, nil
}

func localStateContent(event gonostr.Event, out any) bool {
	if localTag(event, "deleted") == "true" {
		return false
	}
	return json.Unmarshal([]byte(event.Content), out) == nil
}

func localTag(event gonostr.Event, name string) string {
	for _, tag := range event.Tags {
		if len(tag) > 1 && tag[0] == name {
			return tag[1]
		}
	}
	return ""
}
