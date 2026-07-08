package nip65

import "fiatjaf.com/nostr"

// ParseRelayList parses a NIP-65 relay list event (kind 10002) and returns
// separate lists of read and write relays based on the "r" tags.
func ParseRelayList(event nostr.Event) (readRelays []string, writeRelays []string) {
	for tag := range event.Tags.FindAll("r") {
		if len(tag) < 2 {
			continue
		}

		relayURL := tag[1]
		if !nostr.IsValidRelayURL(relayURL) {
			continue
		}

		normalizedURL := nostr.NormalizeURL(relayURL)
		var marker string
		if len(tag) > 2 {
			marker = tag[2]
		}

		if marker == "" || marker == "read" {
			readRelays = append(readRelays, normalizedURL)
		}
		if marker == "" || marker == "write" {
			writeRelays = append(writeRelays, normalizedURL)
		}
	}

	return readRelays, writeRelays
}
