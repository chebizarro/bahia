package signet

import "fiatjaf.com/nostr"

// signetReplyFilter scopes externally written gift-wrap replies to this recipient.
func signetReplyFilter(recipient nostr.PubKey, since nostr.Timestamp) nostr.Filter {
	return nostr.Filter{Kinds: []nostr.Kind{signetKindGiftWrap}, Tags: nostr.TagMap{"p": {recipient.Hex()}}, Since: since}
}
