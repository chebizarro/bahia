---
outline: deep
---

# Management API

[NIP-86](https://nips.nostr.com/86) specifies a set of RPC methods for managing the boring aspects of relays, such as whitelisting or banning users, banning individual events, banning IPs and so on.

All [`khatru.Relay`](https://pkg.go.dev/fiatjaf.com/nostr/khatru#Relay) instances expose a field `ManagementAPI` with a [`RelayManagementAPI`](https://pkg.go.dev/fiatjaf.com/nostr/khatru#RelayManagementAPI) instance inside, which can be used for creating handlers for each of the RPC methods.

There is also a generic `OnAPICall` which is called before any RPC method and, if it returns true, the request is rejected.

The most basic implementation of an `OnAPICall` handler would be one that checks the public key of the caller against a hardcoded public key of the relay owner:

```go
var owner = nostr.MustPubKeyFromHex("<my-own-pubkey>")
var allowedPubkeys = make([]nostr.PubKey, 0, 10)

func main () {
	relay := khatru.NewRelay()

	relay.ManagementAPI.OnAPICall = func(ctx context.Context, mp nip86.MethodParams) (reject bool, msg string) {
		authed, _ := khatru.GetAuthed(ctx)
		if authed != owner {
			return true, "go away, intruder"
		}
		return false, ""
	}

	relay.ManagementAPI.AllowPubKey = func(ctx context.Context, pubkey nostr.PubKey, reason string) error {
		allowedPubkeys = append(allowedPubkeys, pubkey)
		return nil
	}
	relay.ManagementAPI.BanPubKey = func(ctx context.Context, pubkey nostr.PubKey, reason string) error {
		idx := slices.Index(allowedPubkeys, pubkey)
		if idx == -1 {
			return fmt.Errorf("pubkey already not allowed")
		}
		allowedPubkeys = slices.Delete(allowedPubkeys, idx, idx+1)
		return nil
	}
}
```

You can also not provide any `OnAPICall` handler and do the approval specifically on each RPC handler.

In the following example any current member can include any other pubkey, and anyone who was added before is able to remove any pubkey that was added afterwards (not a very good idea, but serves as an example).

```go
var allowedPubkeys = []nostr.PubKey{nostr.MustPubKeyFromHex("<my-own-pubkey>")}

func main () {
	relay := khatru.NewRelay()

	relay.ManagementAPI.AllowPubKey = func(ctx context.Context, pubkey nostr.PubKey, reason string) error {
		caller, _ := khatru.GetAuthed(ctx)

		if slices.Contains(allowedPubkeys, caller) {
			allowedPubkeys = append(allowedPubkeys, pubkey)
			return nil
		}

		return fmt.Errorf("you're not authorized")
	}
	relay.ManagementAPI.BanPubKey = func(ctx context.Context, pubkey nostr.PubKey, reason string) error {
		caller, _ := khatru.GetAuthed(ctx)

		callerIdx := slices.Index(allowedPubkeys, caller)
		if callerIdx == -1 {
			return fmt.Errorf("you're not even allowed here")
		}

		targetIdx := slices.Index(allowedPubkeys, pubkey)
		if targetIdx < callerIdx {
			// target is a bigger OG than the caller, so it has bigger influence and can't be removed
			return fmt.Errorf("you're less powerful than the pubkey you're trying to remove")
		}

		// allow deletion since the target came after the caller
		allowedPubkeys = slices.Delete(allowedPubkeys, targetIdx, targetIdx+1)
		return nil
	}
}
```

By default, `supportedmethods` reports methods that are implemented. This can be customized using `OverwriteSupportedMethods` to for example report only what the authenticated pubkey can do:

```go
relay.OverwriteSupportedMethods = func(ctx context.Context, methods []string) []string {
	pubkey, _ := khatru.GetAuthed(ctx)

	return getSupportedMethodsForPubkey(pubkey)
}
```
