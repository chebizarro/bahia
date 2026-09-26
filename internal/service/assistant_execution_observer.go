package service

import (
	"context"
	"fmt"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
)

// AssistantExecutionObserver owns one scoped backfill-plus-live subscription for
// a submitted request. EOSE is not a terminal operation result.
type AssistantExecutionObserver struct{ Subscriber AssistantRelaySubscriber }

func (o *AssistantExecutionObserver) ObserveAssistantAsyncResult(ctx context.Context, sessionID, toolCallID, toolName string, receipt *domain.AsyncToolReceipt) (AssistantAsyncObservationOutcome, error) {
	if o == nil || o.Subscriber == nil {
		return AssistantAsyncObservationOutcome{Status: "blocked"}, fmt.Errorf("assistant result subscriber unavailable")
	}
	if receipt == nil || receipt.RequestEventID == "" || len(receipt.ResultKinds) == 0 || receipt.ToolName != toolName {
		return AssistantAsyncObservationOutcome{Status: "blocked"}, fmt.Errorf("assistant result receipt invalid")
	}
	kinds := make([]nostr.Kind, len(receipt.ResultKinds))
	for i, k := range receipt.ResultKinds {
		kinds[i] = nostr.Kind(k)
	}
	tags := nostr.TagMap{"e": []string{receipt.RequestEventID}}
	// Resource tags narrow traffic only when the downstream receipt defines them.
	for k, v := range receipt.ResourceTags {
		if k != "" && k != "author" && v != "" {
			tags[k] = []string{v}
		}
	}
	sub, err := o.Subscriber.SubscribeAllWithEOSE(ctx, []nostr.Filter{{Kinds: kinds, Tags: tags}})
	if err != nil {
		return AssistantAsyncObservationOutcome{Status: "blocked"}, err
	}
	defer sub.Close()
	events := sub.EventChan()
	closed := sub.ClosedChan()
	eose := sub.EOSEChan()
	seen := map[string]bool{}
	for {
		select {
		case <-ctx.Done():
			return AssistantAsyncObservationOutcome{Status: "blocked"}, ctx.Err()
		case c, ok := <-closed:
			if !ok {
				closed = nil
				continue
			}
			return AssistantAsyncObservationOutcome{Status: "blocked"}, fmt.Errorf("assistant result subscription closed: %s %s", c.RelayURL, c.Reason)
		case _, ok := <-eose:
			// The relay adapter maintains the subscription for the live stream.
			if !ok {
				eose = nil
			}
		case ev, ok := <-events:
			if !ok {
				return AssistantAsyncObservationOutcome{Status: "blocked"}, fmt.Errorf("assistant result stream ended before terminal event")
			}
			if ev == nil || seen[ev.ID.Hex()] {
				continue
			}
			seen[ev.ID.Hex()] = true
			if !downstreamResultMatchesReceipt(ev, receipt) {
				continue
			}
			matched := true
			for k, v := range receipt.ResourceTags {
				if k != "" && k != "author" && v != "" && !tagContainsValue(ev.Tags, k, v) {
					matched = false
					break
				}
			}
			if !matched {
				continue
			}
			if expected := strings.TrimSpace(receipt.ResourceTags["author"]); expected != "" && ev.PubKey.Hex() != expected {
				continue
			}
			status := terminalStatus(ev)
			if status == "completed" || status == "failed" {
				return AssistantAsyncObservationOutcome{Status: status, Event: ev}, nil
			}
		}
	}
}
