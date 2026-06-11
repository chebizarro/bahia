package nostr

import gonostr "github.com/nbd-wtf/go-nostr"

const (
	SystemDiscoverySchema = "bahia.system-discovery.v1"
	SystemDiscoveryDTag   = "bahia-system-v1"
	SystemDiscoveryName   = "Bahia"

	BrowserRelaySetDTag   = "bahia-browser-v1"
	ContextVMRelaySetDTag = "bahia-contextvm-v1"
	ServiceRelaySetDTag   = "bahia-service-v1"
)

// systemDiscoveryAnnouncementTags is the protocol envelope browsers subscribe to
// and validate. Treat changes as compatibility-impacting: callers must not build
// this tag set ad hoc at publish sites.
func systemDiscoveryAnnouncementTags() gonostr.Tags {
	return gonostr.Tags{
		{"d", SystemDiscoveryDTag},
		{"schema", SystemDiscoverySchema},
		{"name", SystemDiscoveryName},
	}
}

func relaySetTags(dTag string, relays []string) gonostr.Tags {
	tags := gonostr.Tags{{"d", dTag}, {"title", dTag}}
	for _, relay := range normalizeProjectionRelays(relays) {
		tags = append(tags, gonostr.Tag{"relay", relay})
	}
	return tags
}
