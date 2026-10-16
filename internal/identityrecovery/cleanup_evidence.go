package identityrecovery

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"

	"github.com/openagentsinc/bahia/internal/relayguard"
)

func completeCleanup(outcomes map[string]RelayEvidence, relays []string, want string) bool {
	if len(outcomes) != len(relays) {
		return false
	}
	for _, relay := range relays {
		got, ok := outcomes[relayguard.Normalize(relay)]
		if !ok || got.Status != want {
			return false
		}
	}
	return true
}

func cleanupEvidence(s State, phase Phase) []string {
	var evidence []string
	add := func(prefix, relay, id string, outcome RelayEvidence) {
		sum := sha256.Sum256([]byte(relayguard.Normalize(relay)))
		token := prefix + ":" + outcome.Status + ":" + hex.EncodeToString(sum[:])
		if id != "" {
			token += ":" + id
		}
		if outcome.Reason != "" {
			token += ":" + outcome.Reason
		}
		evidence = append(evidence, token)
	}
	switch phase {
	case Inventory:
		for relay, outcome := range s.CleanupInventory {
			add("audit", relay, "", outcome)
		}
	case DeleteAndPurge:
		for id, relays := range s.CleanupDeletions {
			for relay, outcome := range relays {
				add("delete", relay, id, outcome)
			}
		}
	}
	slices.Sort(evidence)
	return evidence
}

func deletionOKEvidence(relay, id string) string {
	sum := sha256.Sum256([]byte(relayguard.Normalize(relay)))
	return "delete:ok:" + hex.EncodeToString(sum[:]) + ":" + id
}
