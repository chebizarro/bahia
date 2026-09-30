package nostrutil

import "errors"

// ErrPublishIncomplete reports that fewer write relays than the caller-facing
// publish quorum (nostr.publish_quorum, default 1) accepted a signed event.
//
// It is not a delivery failure: the signed event is already durable in the
// publish outbox and the publisher keeps retrying the relays that have not
// accepted it. Callers must treat it as "kept, still retrying" and must never
// re-sign and republish the same logical event in response, which would only
// queue a second copy. It lives in this leaf package so producers that cannot
// import the Nostr adapter (import cycle) can still recognise it with
// errors.Is.
var ErrPublishIncomplete = errors.New("nostr event not accepted by the publish quorum")

// ErrPublishAbandoned reports that delivery of a signed event was given up on:
// the publish quorum can no longer be reached (every remaining relay rejected
// it permanently, or the attempt budget ran out), its outbox row is failed and
// nothing retries it. It never matches ErrPublishIncomplete. Callers must
// record a terminal failure, not "queued"; producing the content again means
// signing a new event.
var ErrPublishAbandoned = errors.New("nostr event delivery abandoned")

// IsPublishQueued reports whether err means the event was durably queued for
// redelivery rather than lost (see ErrPublishIncomplete).
func IsPublishQueued(err error) bool {
	return errors.Is(err, ErrPublishIncomplete)
}

// IsPublishAbandoned reports whether err means delivery was given up on and
// the event is no longer being retried (see ErrPublishAbandoned).
func IsPublishAbandoned(err error) bool {
	return errors.Is(err, ErrPublishAbandoned)
}
