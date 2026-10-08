package nip77

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip77/negentropy"
	"fiatjaf.com/nostr/nip77/negentropy/storage/vector"
)

type Direction struct {
	From  nostr.Querier
	To    nostr.Publisher
	Items chan nostr.ID
}

func NegentropySync(
	ctx context.Context,

	relayUrl string,
	filter nostr.Filter,

	// where our local events will be read from.
	// if it is nil the sync will be unidirectional: download-only.
	source nostr.Querier,

	// where new events received from the relay will be written to.
	// if it is nil the sync will be unidirectional: upload-only.
	// it can also be a nostr.QuerierPublisher in case source isn't provided
	// and you need a download-only sync that respects local data.
	target nostr.Publisher,

	// handle ids received on each direction, usually called with Sync() so the corresponding events are
	// fetched from the source and published to the target
	handle func(ctx context.Context, directions Direction),
) error {
	return NegentropySyncWithOptions(ctx, relayUrl, filter, source, target, handle, nostr.RelayOptions{})
}

// NegentropySyncWithOptions is NegentropySync with the session connection's
// options (Bahia patch, see BAHIA_PATCHES.md). With options.AuthHandler set the
// connection answers the relay's NIP-42 challenge, and a NEG-ERR whose reason
// starts with "auth-required:" makes the session authenticate (joining an
// AUTH attempt already in flight) and send its NEG-OPEN again, once: a second
// refusal ends the session with that NEG-ERR. options.CustomHandler, if set,
// still sees every frame the session does not handle itself.
func NegentropySyncWithOptions(
	ctx context.Context,
	relayUrl string,
	filter nostr.Filter,
	source nostr.Querier,
	target nostr.Publisher,
	handle func(ctx context.Context, directions Direction),
	options nostr.RelayOptions,
) error {
	id := "nl-tmp" // for now we can't have more than one subscription in the same connection

	vec := vector.New()
	neg := negentropy.New(vec, 60_000, source != nil, target != nil)
	// Release blocked id producers when the session ends for any reason
	// (completion, NEG-ERR, timeout, cancellation): the consumer may stop
	// reading before a reconcile closes the channels (Bahia patch, see
	// BAHIA_PATCHES.md).
	defer neg.Release()

	// fill our local vector and build the NEG-OPEN before dialing, so the
	// frame handler below never sees a session that is still being set up
	// (Bahia patch).
	var usedSource nostr.Querier
	if source != nil {
		for evt := range source.QueryEvents(filter) {
			vec.Insert(evt.CreatedAt, evt.ID)
		}
		usedSource = source
	}
	if target != nil {
		if targetSource, ok := target.(nostr.Querier); ok && targetSource != usedSource {
			for evt := range targetSource.QueryEvents(filter) {
				vec.Insert(evt.CreatedAt, evt.ID)
			}
		}
	}
	vec.Seal()
	open, _ := OpenEnvelope{id, filter, neg.Start()}.MarshalJSON()

	// errch holds the first outcome. report never blocks: it runs on the
	// relay's read loop, and a second outcome (a late NEG-ERR, a relay CLOSE
	// after completion) arriving once nobody reads errch must not wedge that
	// loop (Bahia patch, see BAHIA_PATCHES.md).
	errch := make(chan error, 1)
	report := func(err error) {
		select {
		case errch <- err:
		default:
		}
	}

	var relay *nostr.Relay
	var reopened atomic.Bool
	authHandler := options.AuthHandler
	customHandler := options.CustomHandler
	options.CustomHandler = func(data string) {
		envelope := ParseNegMessage(data)
		if envelope == nil {
			if customHandler != nil {
				customHandler(data)
			}
			return
		}
		switch env := envelope.(type) {
		case *OpenEnvelope, *CloseEnvelope:
			report(fmt.Errorf("unexpected %s received from relay", env.Label()))
			return
		case *ErrorEnvelope:
			if authHandler != nil && isAuthRequired(env.Reason) && reopened.CompareAndSwap(false, true) {
				// Relay.Auth waits for the relay's OK, which this read loop
				// delivers: authenticate and re-open from another goroutine.
				go func() {
					err := relay.Auth(ctx, func(ctx context.Context, evt *nostr.Event) error {
						return authHandler(ctx, relay, evt)
					})
					if err != nil {
						report(fmt.Errorf("relay returned a %s: %s (NIP-42 AUTH failed: %w)", env.Label(), env.Reason, err))
						return
					}
					if err := relay.WriteWithError(open); err != nil {
						report(fmt.Errorf("failed to re-open negentropy after AUTH: %w", err))
					}
				}()
				return
			}
			report(fmt.Errorf("relay returned a %s: %s", env.Label(), env.Reason))
			return
		case *MessageEnvelope:
			nextmsg, err := neg.Reconcile(env.Message)
			if err != nil {
				report(fmt.Errorf("failed to reconcile: %w", err))
				return
			}

			if nextmsg != "" {
				msgb, _ := MessageEnvelope{id, nextmsg}.MarshalJSON()
				relay.Write(msgb)
			}
		}
	}

	// connect to relay. NewRelay binds the connection to a background
	// context, so it stays open until Close: close it when the sync ends,
	// also after a failed dial (Bahia patch).
	relay = nostr.NewRelay(context.Background(), relayUrl, options)
	defer relay.Close()
	if err := relay.Connect(ctx); err != nil {
		return err
	}

	// kickstart the process
	if err := relay.WriteWithError(open); err != nil {
		return fmt.Errorf("failed to write to relay: %w", err)
	}

	defer func() {
		clse, _ := CloseEnvelope{id}.MarshalJSON()
		relay.Write(clse)
	}()

	wg := sync.WaitGroup{}

	// handle emitted events from either direction
	if source != nil {
		wg.Go(func() {
			handle(ctx, Direction{
				From:  source,
				To:    relay,
				Items: neg.Haves,
			})
		})
	}
	if target != nil {
		wg.Go(func() {
			handle(ctx, Direction{
				From:  relay,
				To:    target,
				Items: neg.HaveNots,
			})
		})
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		wg.Wait()
		report(nil)
	}()

	select {
	case err := <-errch:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

// isAuthRequired reports whether a NEG-ERR reason carries NIP-42's
// machine-readable "auth-required:" prefix (Bahia patch).
func isAuthRequired(reason string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(reason)), "auth-required:")
}

func SyncEventsFromIDs(ctx context.Context, dir Direction) {
	// this is only necessary because relays are too ratelimiting
	batch := make([]nostr.ID, 0, 50)

	seen := make(map[nostr.ID]struct{})
	for item := range dir.Items {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}

		batch = append(batch, item)
		if len(batch) == 50 {
			for evt := range dir.From.QueryEvents(nostr.Filter{IDs: batch}) {
				dir.To.Publish(ctx, evt)
			}
			batch = batch[:0]
		}
	}

	if len(batch) > 0 {
		for evt := range dir.From.QueryEvents(nostr.Filter{IDs: batch}) {
			dir.To.Publish(ctx, evt)
		}
	}
}
