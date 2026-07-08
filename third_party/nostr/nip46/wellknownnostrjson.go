package nip46

import (
	"context"
	"fmt"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip05"
)

func queryWellKnownNostrJson(ctx context.Context, fullname string) (pubkey nostr.PubKey, relays []string, err error) {
	result, name, err := nip05.Fetch(ctx, fullname)
	if err != nil {
		return nostr.ZeroPK, nil, err
	}

	pubkeyh, ok := result.Names[name]
	if !ok {
		return nostr.ZeroPK, nil, fmt.Errorf("no entry found for the '%s' name", name)
	}
	relays, _ = result.NIP46[pubkeyh]
	if !ok {
		return nostr.ZeroPK, nil, fmt.Errorf("no bunker relays found for the '%s' name", name)
	}

	return pubkey, relays, nil
}
