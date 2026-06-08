package docs

import (
	"context"

	"github.com/nbd-wtf/go-nostr"
)

// DocsQuerierFunc is a convenience adapter that turns a function into a
// NostrDocsQuerier. The app wiring layer can create a closure that captures the
// relay pool and pubkey without the docs package importing the nostr adapter.
type DocsQuerierFunc func(ctx context.Context, pubkey string) ([]*nostr.Event, error)

// QueryDocEvents delegates to the wrapped function.
func (f DocsQuerierFunc) QueryDocEvents(ctx context.Context, pubkey string) ([]*nostr.Event, error) {
	return f(ctx, pubkey)
}
