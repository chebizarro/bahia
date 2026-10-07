package soulfactory

import (
	"context"
	"errors"
	"fmt"
	"time"

	"fiatjaf.com/nostr"
)

const (
	// DefaultRuntimeControlResultTimeout bounds how long a runtime adapter waits
	// for the correlated kind:38386 result after its control request was
	// accepted. RuntimeAdapterConfig.ResultTimeout (soul_factory.runtime_result_timeout)
	// overrides it.
	DefaultRuntimeControlResultTimeout = 5 * time.Minute
	// DefaultSoulFactoryReplyTimeout bounds how long NostrClient waits for a
	// provisioning or soul action terminal result. NostrClient.WithReplyTimeout
	// (soul_factory.reply_timeout; the CLI's --reply-timeout) overrides it.
	DefaultSoulFactoryReplyTimeout = 15 * time.Minute
)

// ErrNoTerminalResult matches every *NoTerminalResultError.
var ErrNoTerminalResult = errors.New("no terminal result observed")

// NoTerminalResultError reports that a request was published but its wait for
// a terminal reply ended first: the outcome is unknown, neither success nor
// failure. Terminal replies are durable, regular Nostr events tagged with the
// request id, so a result published later is not lost:
//
// - NostrClient: call AwaitProvisioningResult (with the same receipt) or
// AwaitSoulActionResult (with RequestID) again. The new subscription's
// stored-event backfill returns the result if it has been published.
// - Runtime provisioning: Reactor.Run subscribes to kind:38386 results and
// handleLateRuntimeResult projects a late provisioning success.
// - Runtime lifecycle actions and fleet config reloads: the operation is
// parked as awaiting_terminal, never rolled back on the timeout alone, and
// the late result is reconciled through runtimeResultWaiters.
type NoTerminalResultError struct {
	// RequestID is the published request's event id.
	RequestID string
	// Timeout is the configured wait bound. The caller's context may have ended
	// the wait earlier; Cause says which.
	Timeout time.Duration
	// Cause is context.DeadlineExceeded or context.Canceled from the wait, or
	// the reason the reply subscription ended.
	Cause error
}

func (e *NoTerminalResultError) Error() string {
	return fmt.Sprintf("no terminal result observed for request %s within %s: %v", e.RequestID, e.Timeout, e.Cause)
}

func (e *NoTerminalResultError) Unwrap() []error {
	if e.Cause == nil {
		return []error{ErrNoTerminalResult}
	}
	return []error{ErrNoTerminalResult, e.Cause}
}

// errReplySubscriptionClosed is the cause when the relay client ends the reply
// subscription before the wait does.
var errReplySubscriptionClosed = errors.New("reply subscription closed")

// replyClass is how a reply wait treats one event.
type replyClass int

const (
	replyIgnore replyClass = iota
	replyStatus
	replyTerminal
)

// awaitTerminalReply reads sub until classify reports a terminal event, which
// it returns. Status events go to onStatus. The wait is bounded by timeout
// (defaultTimeout when timeout is not positive) or ctx's earlier deadline, and
// ends with *NoTerminalResultError. EOSE is irrelevant: the reply may be stored
// or arrive live.
func awaitTerminalReply(ctx context.Context, sub *RelaySubscription, requestID string, timeout, defaultTimeout time.Duration, classify func(*nostr.Event) replyClass, onStatus func(*nostr.Event)) (*nostr.Event, error) {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	seen := map[nostr.ID]struct{}{}
	for {
		select {
		case <-waitCtx.Done():
			return nil, &NoTerminalResultError{RequestID: requestID, Timeout: timeout, Cause: context.Cause(waitCtx)}
		case event, ok := <-sub.Events:
			if !ok {
				cause := errReplySubscriptionClosed
				if waitCtx.Err() != nil {
					cause = context.Cause(waitCtx)
				}
				return nil, &NoTerminalResultError{RequestID: requestID, Timeout: timeout, Cause: cause}
			}
			if event == nil {
				continue
			}
			if _, duplicate := seen[event.ID]; duplicate {
				continue
			}
			seen[event.ID] = struct{}{}
			switch classify(event) {
			case replyStatus:
				if onStatus != nil {
					onStatus(event)
				}
			case replyTerminal:
				return event, nil
			}
		}
	}
}
