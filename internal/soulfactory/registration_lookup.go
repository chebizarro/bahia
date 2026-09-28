package soulfactory

import (
	"context"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
)

// IsRegisteredAgent checks the current Soul Factory read models rather than a
// startup snapshot, so revocation and newly provisioned identities take effect
// for consumers of this registration boundary without restarting Bahia.
func (r *Reactor) IsRegisteredAgent(ctx context.Context, pubkey string) (bool, error) {
	if r == nil || strings.TrimSpace(pubkey) == "" {
		return false, nil
	}
	souls, err := r.listFleetReconcileSouls(ctx)
	if err != nil {
		return false, err
	}
	for _, soul := range souls {
		if soul != nil && soul.Status == domain.SoulStatusActive && strings.EqualFold(soul.NostrPubkey, pubkey) {
			return true, nil
		}
	}
	return false, nil
}
