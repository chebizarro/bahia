// Package bypass is a guard fixture: every function here writes an outbound
// relay frame around the admission layer in a different syntactic disguise.
// It is never built into Bahia (testdata is excluded from ./...).
package bypass

import (
	"context"

	gn "fiatjaf.com/nostr"
	remote "fiatjaf.com/nostr/nip46"
	"fiatjaf.com/nostr/nip77"
	casnostr "git.sharegap.net/cascadia/cascadia-go/nostr"
	"github.com/coder/websocket"
)

func AliasedRelayPublish(ctx context.Context, r *gn.Relay, ev gn.Event) error {
	return r.Publish(ctx, ev)
}

func MethodValue(ctx context.Context, r *gn.Relay, ev gn.Event) error {
	send := r.Publish
	return send(ctx, ev)
}

func MethodExpression(ctx context.Context, r *gn.Relay, ev gn.Event) error {
	return (*gn.Relay).Publish(r, ctx, ev)
}

func ConnectThenPublish(ctx context.Context, url string, ev gn.Event) error {
	r, err := gn.RelayConnect(ctx, url, gn.RelayOptions{})
	if err != nil {
		return err
	}
	return r.Publish(ctx, ev)
}

func PoolFanOut(ctx context.Context, p *gn.Pool, urls []string, ev gn.Event) {
	for range p.PublishMany(ctx, urls, ev) {
	}
}

func CascadiaHelper(ctx context.Context, urls []string, ev casnostr.Event) {
	_, _ = casnostr.Publish(ctx, urls, ev)
}

func RawBunker(ctx context.Context, sk gn.SecretKey, uri string) {
	b, _ := remote.ConnectBunker(ctx, sk, uri, nil, nil)
	_, _ = b.GetPublicKey(ctx)
}

// AuthBypass answers a NIP-42 challenge with a bare signer: the AUTH frame
// reaches the wire without an admission permit (the pre-fix pool shape).
func AuthBypass(ctx context.Context, r *gn.Relay, sk gn.SecretKey) error {
	return r.Auth(ctx, func(_ context.Context, ev *gn.Event) error {
		return ev.Sign(sk)
	})
}

// AuthMethodValue smuggles Relay.Auth out of a call position.
func AuthMethodValue(r *gn.Relay) func(context.Context, func(context.Context, *gn.Event) error) error {
	return r.Auth
}

// RawFrameWrite hand-builds a frame — anything from an EVENT to a NEG-MSG —
// and writes it straight onto the relay connection.
func RawFrameWrite(r *gn.Relay, frame []byte) error {
	r.Write(frame)
	return r.WriteWithError(frame)
}

// NegentropySessionUpload starts a NIP-77 session whose upload direction
// publishes local-only events directly to the raw relay connection.
func NegentropySessionUpload(ctx context.Context, url string, filter gn.Filter, source gn.Querier, target gn.Publisher) error {
	return nip77.NegentropySyncWithOptions(ctx, url, filter, source, target,
		func(ctx context.Context, dir nip77.Direction) {
			nip77.SyncEventsFromIDs(ctx, dir)
		}, gn.RelayOptions{})
}

// PublisherInterfaceUpload publishes through the nostr.Publisher interface,
// whose value may be a raw relay: the frame writer is invisible to a
// concrete-type scan.
func PublisherInterfaceUpload(ctx context.Context, to gn.Publisher, ev gn.Event) error {
	return to.Publish(ctx, ev)
}

// RawWebsocketWrite dials a relay itself and writes frames on the bare
// websocket, below every library type.
func RawWebsocketWrite(ctx context.Context, url string, frame []byte) error {
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	return conn.Write(ctx, websocket.MessageText, frame)
}
