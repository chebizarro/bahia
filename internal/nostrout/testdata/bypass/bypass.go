// Package bypass is a guard fixture: every function here publishes around
// the admission layer in a different syntactic disguise. It is never built
// into Bahia (testdata is excluded from ./...).
package bypass

import (
	"context"

	gn "fiatjaf.com/nostr"
	remote "fiatjaf.com/nostr/nip46"
	casnostr "git.sharegap.net/cascadia/cascadia-go/nostr"
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
