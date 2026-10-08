package khatru

import (
	"context"
	"errors"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip45/hyperloglog"
)

// checkRequest runs the REQ filter acceptance policy (OnRequest) and returns
// the rejection reason, if any. Bahia patch (listener-before-query, see
// BAHIA_PATCHES.md): the policy for every filter of a REQ runs before any live
// listener is registered and before any stored query runs, so a rejected REQ
// never exposes a listener, even transiently.
func (rl *Relay) checkRequest(ctx context.Context, filter nostr.Filter) error {
	// then check if we'll reject this filter (we apply this after overwriting
	// because we may, for example, remove some things from the incoming filters
	// that we know we don't support, and then if the end result is an empty
	// filter we can just reject it)
	if nil != rl.OnRequest {
		if reject, msg := rl.OnRequest(ctx, filter); reject {
			return errors.New(nostr.NormalizeOKMessage(msg, "blocked"))
		}
	}
	return nil
}

// queryStoredEvents dispatches one filter's stored events as they're loaded
// from the database. Bahia patch (listener-before-query): it runs after the
// filter's live listener was registered, so an event saved while the query
// runs is broadcast to the subscription instead of falling into a gap where
// it is neither replayed nor broadcast. An event that ends up both replayed
// and broadcast is a duplicate the client deduplicates.
func (rl *Relay) queryStoredEvents(ctx context.Context, id string, ws *WebSocket, filter nostr.Filter) {
	if filter.LimitZero {
		// don't do any queries, just subscribe to future events
		return
	}

	// run the function to query events
	if nil != rl.QueryStored {
		for event := range rl.QueryStored(ctx, filter) {
			if nil != ws.WriteJSON(nostr.EventEnvelope{SubscriptionID: &id, Event: event}) {
				break
			}
		}
	}
}

func (rl *Relay) handleCountRequest(ctx context.Context, ws *WebSocket, filter nostr.Filter) uint32 {
	// check if we'll reject this filter
	if nil != rl.OnCount {
		if rejecting, msg := rl.OnCount(ctx, filter); rejecting {
			ws.WriteJSON(nostr.NoticeEnvelope(msg))
			return 0
		}
	}

	// run the functions to count (generally it will be just one)
	if nil != rl.Count {
		res, err := rl.Count(ctx, filter)
		if err != nil {
			ws.WriteJSON(nostr.NoticeEnvelope(err.Error()))
		}
		return res
	}

	return 0
}

func (rl *Relay) handleCountRequestWithHLL(
	ctx context.Context,
	ws *WebSocket,
	filter nostr.Filter,
	offset int,
) (uint32, *hyperloglog.HyperLogLog) {
	// check if we'll reject this filter
	if nil != rl.OnCount {
		if rejecting, msg := rl.OnCount(ctx, filter); rejecting {
			ws.WriteJSON(nostr.NoticeEnvelope(msg))
			return 0, nil
		}
	}

	if nil != rl.CountHLL {
		res, hll, err := rl.CountHLL(ctx, filter, offset)
		if err != nil {
			ws.WriteJSON(nostr.NoticeEnvelope(err.Error()))
		}
		return res, hll
	}

	return 0, nil
}
