package app

import (
	"fiatjaf.com/nostr"

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

// inboundSyncConfigScoped is inboundSyncConfig with negentropy upload scoped to
// the daemon's own service relays. Control-plane events in the local store are
// not pushed to interop relays that happen to be in the subscription pool.
func inboundSyncConfigScoped(store config.NostrLocalStoreConfig, serviceRelays []string) nostrAdapter.InboundSyncConfig {
	sync := inboundSyncConfig(store)
	if sync.NegentropyUpload && len(serviceRelays) > 0 {
		allowed := make(map[string]struct{}, len(serviceRelays))
		for _, url := range serviceRelays {
			allowed[nostr.NormalizeURL(url)] = struct{}{}
		}
		sync.NegentropyUploadFilter = func(relayURL string) bool {
			_, ok := allowed[nostr.NormalizeURL(relayURL)]
			return ok
		}
	}
	return sync
}
