package relaysidecar

import (
	"strings"
	"testing"

	"fiatjaf.com/nostr"
)

func TestRelayQuerySQLScopesCanonicalDiscoveryBeforeReplay(t *testing.T) {
	author := nostr.PubKey{1}
	filter := nostr.Filter{
		Kinds:   []nostr.Kind{11316, 30002},
		Authors: []nostr.PubKey{author},
		Since:   100,
		Until:   200,
	}

	query, args := relayQuerySQL(filter)
	for _, fragment := range []string{
		"kind IN (?,?)",
		"pubkey IN (?)",
		"created_at >= ?",
		"created_at <= ?",
		"ORDER BY created_at DESC, id",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("query %q missing %q", query, fragment)
		}
	}
	if len(args) != 5 {
		t.Fatalf("expected 5 query arguments, got %d: %#v", len(args), args)
	}
}

func TestRelayQuerySQLEmptyExplicitKindsMatchesNothing(t *testing.T) {
	query, _ := relayQuerySQL(nostr.Filter{Kinds: []nostr.Kind{}})
	if !strings.Contains(query, "1 = 0") {
		t.Fatalf("explicit empty kinds must match nothing: %q", query)
	}
}
