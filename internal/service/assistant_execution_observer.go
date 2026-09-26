package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

// AssistantObservationSignal reports subscription health for one receipt. It
// never carries a downstream outcome: Blocked means the observer cannot
// currently see the relay stream, not that the operation failed.
type AssistantObservationSignal struct {
	Blocked bool
	Reason  string
}

// AssistantWorkObservationRequest scopes one observation to run/work/request.
type AssistantWorkObservationRequest struct {
	SessionID string
	RunID     string
	WorkID    string
	ToolName  string
	Receipt   *domain.AsyncToolReceipt
	// OnSignal, when set, receives blocked/live transitions. It is called
	// synchronously from the observing goroutine and must not block.
	OnSignal func(AssistantObservationSignal)
}

// AssistantWorkObserver resolves a submitted request to its terminal event.
// It returns only a terminal outcome, or an error when its context ends or the
// receipt cannot be observed at all. Elapsed time never decides the outcome.
type AssistantWorkObserver interface {
	ObserveWork(context.Context, AssistantWorkObservationRequest) (AssistantAsyncObservationOutcome, error)
}

// AssistantExecutionObserver owns one scoped backfill-plus-live subscription
// per submitted request. EOSE completes backfill only; the subscription stays
// open. CLOSED/AUTH loss and stream termination block observation and the
// subscription is reissued after reconnect backoff, keeping event-ID dedupe.
type AssistantExecutionObserver struct {
	Subscriber AssistantRelaySubscriber
	// ReissueWait waits before reissuing a subscription that ended. The default
	// is capped exponential reconnect backoff; tests inject a barrier.
	ReissueWait func(ctx context.Context, attempt int) error
}

var _ AssistantWorkObserver = (*AssistantExecutionObserver)(nil)

// ObserveAssistantAsyncResult adapts the observer to the legacy runtime
// observer interface.
func (o *AssistantExecutionObserver) ObserveAssistantAsyncResult(ctx context.Context, sessionID, toolCallID, toolName string, receipt *domain.AsyncToolReceipt) (AssistantAsyncObservationOutcome, error) {
	return o.ObserveWork(ctx, AssistantWorkObservationRequest{SessionID: sessionID, WorkID: toolCallID, ToolName: toolName, Receipt: receipt})
}

func (o *AssistantExecutionObserver) ObserveWork(ctx context.Context, req AssistantWorkObservationRequest) (AssistantAsyncObservationOutcome, error) {
	blocked := AssistantAsyncObservationOutcome{Status: "blocked"}
	if o == nil || o.Subscriber == nil {
		return blocked, errors.New("assistant result subscriber unavailable")
	}
	receipt := req.Receipt
	if receipt == nil || receipt.RequestEventID == "" || len(receipt.ResultKinds) == 0 || receipt.ToolName != req.ToolName {
		return blocked, errors.New("assistant result receipt invalid")
	}
	signal := func(sig AssistantObservationSignal) {
		if req.OnSignal != nil {
			req.OnSignal(sig)
		}
	}
	filter := assistantReceiptFilter(receipt)
	seen := map[string]bool{}
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			if err := o.wait(ctx, attempt); err != nil {
				return blocked, err
			}
		}
		sub, err := o.Subscriber.SubscribeAllWithEOSE(ctx, []nostr.Filter{filter})
		if err != nil {
			if ctx.Err() != nil {
				return blocked, ctx.Err()
			}
			signal(AssistantObservationSignal{Blocked: true, Reason: "subscribe: " + err.Error()})
			continue
		}
		outcome, terminal, err := observeAssistantSubscription(ctx, sub, receipt, seen, signal)
		sub.Close()
		if terminal || err != nil {
			return outcome, err
		}
		// Every relay ended the subscription without a terminal event. That is
		// loss of visibility, not evidence about the downstream operation.
		signal(AssistantObservationSignal{Blocked: true, Reason: "result subscription ended; reissuing"})
	}
}

func (o *AssistantExecutionObserver) wait(ctx context.Context, attempt int) error {
	if o.ReissueWait != nil {
		return o.ReissueWait(ctx, attempt)
	}
	delay := time.Second << min(attempt-1, 6)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func assistantReceiptFilter(receipt *domain.AsyncToolReceipt) nostr.Filter {
	kinds := make([]nostr.Kind, len(receipt.ResultKinds))
	for i, k := range receipt.ResultKinds {
		kinds[i] = nostr.Kind(k)
	}
	tags := nostr.TagMap{"e": []string{receipt.RequestEventID}}
	// Resource tags narrow relay traffic only when the receipt defines them.
	for k, v := range receipt.ResourceTags {
		if k != "" && k != "author" && v != "" {
			tags[k] = []string{v}
		}
	}
	filter := nostr.Filter{Kinds: kinds, Tags: tags}
	if author := strings.TrimSpace(receipt.ResourceTags["author"]); author != "" {
		if pk, err := nostr.PubKeyFromHex(author); err == nil {
			filter.Authors = []nostr.PubKey{pk}
		}
	}
	return filter
}

// observeAssistantSubscription consumes one subscription. It returns
// terminal=true with the outcome, or terminal=false when the stream ended.
func observeAssistantSubscription(ctx context.Context, sub AssistantMergedSubscription, receipt *domain.AsyncToolReceipt, seen map[string]bool, signal func(AssistantObservationSignal)) (AssistantAsyncObservationOutcome, bool, error) {
	events := sub.EventChan()
	closed := sub.ClosedChan()
	eose := sub.EOSEChan()
	relayClosed := false
	for {
		select {
		case <-ctx.Done():
			return AssistantAsyncObservationOutcome{Status: "blocked"}, false, ctx.Err()
		case c, ok := <-closed:
			if !ok {
				closed = nil
				continue
			}
			// A relay refused or dropped the subscription (including
			// auth-required). Other relays may continue; the pool reissues on
			// reconnect. Observation is blocked until a fresh backfill.
			relayClosed = true
			signal(AssistantObservationSignal{Blocked: true, Reason: fmt.Sprintf("relay %s closed subscription: %s", c.RelayURL, c.Reason)})
		case <-eose:
			// Backfill complete; keep the subscription open for live events.
			eose = nil
			if !relayClosed {
				signal(AssistantObservationSignal{Blocked: false, Reason: "backfill complete; observing live"})
			}
		case ev, ok := <-events:
			if !ok {
				return AssistantAsyncObservationOutcome{}, false, nil
			}
			if ev == nil || seen[ev.ID.Hex()] {
				continue
			}
			seen[ev.ID.Hex()] = true
			if !assistantResultMatchesReceipt(ev, receipt) {
				continue
			}
			status := terminalStatus(ev)
			if status == "completed" || status == "failed" {
				return AssistantAsyncObservationOutcome{Status: status, Event: ev}, true, nil
			}
		}
	}
}

// assistantResultMatchesReceipt validates ID, signature, kind, request
// correlation, declared resource tags and expected author before trust.
func assistantResultMatchesReceipt(ev *nostr.Event, receipt *domain.AsyncToolReceipt) bool {
	if !downstreamResultMatchesReceipt(ev, receipt) {
		return false
	}
	for k, v := range receipt.ResourceTags {
		if k != "" && k != "author" && v != "" && !tagContainsValue(ev.Tags, k, v) {
			return false
		}
	}
	if expected := strings.TrimSpace(receipt.ResourceTags["author"]); expected != "" && ev.PubKey.Hex() != expected {
		return false
	}
	return true
}
