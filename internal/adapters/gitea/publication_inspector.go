package gitea

import (
	"context"
	"fmt"

	"fiatjaf.com/nostr"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
)

type publicationSubscriber interface {
	SubscribeAllWithEOSE(context.Context, []nostr.Filter) (*nostrAdapter.MergedSubscription, error)
}

type RelayPublicationInspector struct{ subscriber publicationSubscriber }

func NewRelayPublicationInspector(subscriber publicationSubscriber) *RelayPublicationInspector {
	return &RelayPublicationInspector{subscriber: subscriber}
}

// FindPublishedEvent inspects a scoped historical subscription through EOSE.
// It never interprets timeout, CLOSED, or disconnected streams as absence.
func (r *RelayPublicationInspector) FindPublishedEvent(ctx context.Context, filter nostr.Filter) (*nostr.Event, error) {
	sub, err := r.subscriber.SubscribeAllWithEOSE(ctx, []nostr.Filter{filter})
	if err != nil {
		return nil, err
	}
	defer sub.Close()
	events, closed := sub.Events, sub.Closed
	var found *nostr.Event
	accept := func(event *nostr.Event) error {
		if event == nil || !filter.Matches(*event) || !event.CheckID() || !event.VerifySignature() {
			return fmt.Errorf("invalid publication evidence")
		}
		if found != nil && found.ID != event.ID {
			return fmt.Errorf("ambiguous publication evidence")
		}
		found = event
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case reason, ok := <-closed:
			if !ok {
				closed = nil
				continue
			}
			return nil, fmt.Errorf("publication inspection CLOSED: %s", reason.Reason)
		case event, ok := <-events:
			if !ok {
				return nil, fmt.Errorf("publication inspection stream ended before EOSE")
			}
			if err := accept(event); err != nil {
				return nil, err
			}
		case <-sub.EndOfStoredEvents:
			if !sub.AllRelaysReachedEOSE() {
				return nil, fmt.Errorf("publication inspection did not reach complete EOSE")
			}
			// EOSE and the last buffered EVENT can be ready simultaneously.
			for {
				select {
				case event, ok := <-events:
					if !ok {
						return found, nil
					}
					if err := accept(event); err != nil {
						return nil, err
					}
				default:
					return found, nil
				}
			}
		}
	}
}
