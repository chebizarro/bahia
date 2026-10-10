package config

import (
	"os"
	"strings"

	"github.com/openagentsinc/bahia/internal/nostrout"
)

// Admission maps nostr.outbound onto the process-wide outbound admission
// controller configuration. Every Bahia process that reads this config
// (bahia-server and bahia-relay) initializes nostrout from it, so a lane or
// kill switch configured here bounds all of the process's publications and
// NIP-46 signer requests. Zero values keep nostrout's bounded defaults; no
// configured value can disable admission. A kill switch file left unset
// still resolves from BAHIA_NOSTR_OUTBOUND_KILL_SWITCH_FILE for configs that
// did not go through Load's environment mapping.
func (c NostrOutboundConfig) Admission() nostrout.Config {
	lane := func(l NostrOutboundLaneConfig) nostrout.PurposeBudget {
		return nostrout.PurposeBudget{RatePerMinute: l.RatePerMinute, Burst: l.Burst}
	}
	killSwitchFile := strings.TrimSpace(c.KillSwitchFile)
	if killSwitchFile == "" {
		killSwitchFile = strings.TrimSpace(os.Getenv(nostrout.KillSwitchEnv))
	}
	return nostrout.Config{
		KillSwitchFile: killSwitchFile,
		Aggregate:      lane(c.Aggregate),
		PurposeBudgets: map[nostrout.Purpose]nostrout.PurposeBudget{
			nostrout.PurposePriority: lane(c.Lanes.Priority),
			nostrout.PurposeState:    lane(c.Lanes.State),
			nostrout.PurposeGeneral:  lane(c.Lanes.General),
			nostrout.PurposeBulk:     lane(c.Lanes.Bulk),
			nostrout.PurposeSigner:   lane(c.Lanes.Signer),
		},
		RelayWire:         lane(c.RelayWire),
		RelayWirePriority: lane(c.RelayWirePriority),
		BreakerMin:        c.BreakerMin,
		BreakerMax:        c.BreakerMax,
		DuplicateTTL:      c.DuplicateTTL,
		DuplicateLimit:    c.DuplicateLimit,
	}
}
