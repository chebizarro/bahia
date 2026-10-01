package app

import (
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
)

// inboundSyncConfig maps nostr.local_store onto the inbound sync tuning used
// by the subscriber and the bootstrapper's live groups.
func inboundSyncConfig(store config.NostrLocalStoreConfig) nostrAdapter.InboundSyncConfig {
	sync := nostrAdapter.DefaultInboundSyncConfig()
	sync.ResumeOverlap = store.ResumeOverlap
	sync.RegularLookback = store.RegularLookback
	sync.NegentropyUpload = store.NegentropyUpload
	return sync
}
