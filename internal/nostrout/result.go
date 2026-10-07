package nostrout

import (
	"strings"
)

// Result is one relay's outcome for one EVENT publication. Protocol OK=false
// rejections preserve the relay-provided Reason with Error unset; transport and
// connection failures preserve Error with Reason unset. Cached marks a result
// synthesized from a local receipt: no frame was sent.
type Result struct {
	RelayURL string
	Accepted bool
	Reason   string // rejection reason if not accepted
	Error    error  // transport/connection error (nil if relay responded)
	Cached   bool
}

// IsAuthRequiredReason returns true if a relay protocol reason requires authentication.
func IsAuthRequiredReason(reason string) bool {
	normalized := strings.ToLower(strings.TrimSpace(reason))
	return normalized == "auth-required" || strings.HasPrefix(normalized, "auth-required:")
}

// IsRateLimitedReason returns true if a relay protocol reason indicates rate limiting.
func IsRateLimitedReason(reason string) bool {
	return strings.HasPrefix(strings.TrimSpace(reason), "rate-limited:")
}

// IsBlockedReason returns true if a relay protocol reason indicates a policy block.
func IsBlockedReason(reason string) bool {
	return strings.HasPrefix(reason, "blocked:")
}

// IsDuplicateReason returns true if a relay already has the event.
func IsDuplicateReason(reason string) bool {
	return strings.HasPrefix(reason, "duplicate:")
}

// IsRateLimited returns true if the relay is rate-limiting.
func (r Result) IsRateLimited() bool { return IsRateLimitedReason(r.Reason) }

// IsDuplicate returns true if the relay already has this event.
func (r Result) IsDuplicate() bool { return IsDuplicateReason(r.Reason) }

// Succeeded reports relay acceptance or an equivalent duplicate receipt.
func (r Result) Succeeded() bool { return r.Accepted || r.IsDuplicate() }

// ProtocolRejectionReason extracts a NIP-01 OK=false reason from the error the
// Nostr library returns for a rejected publish ("msg: <reason>"). Arbitrary
// transport errors are not relay protocol ACKs and return false.
func ProtocolRejectionReason(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	message := err.Error()
	if !strings.HasPrefix(message, "msg:") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(message, "msg:")), true
}

// ResultFromPublishError converts a library publish outcome into a Result.
func ResultFromPublishError(relayURL string, err error) Result {
	result := Result{RelayURL: relayURL}
	switch reason, ok := ProtocolRejectionReason(err); {
	case err == nil:
		result.Accepted = true
	case ok:
		result.Reason = reason
	default:
		result.Error = err
	}
	return result
}

const cachedDuplicateReason = "duplicate: suppressed locally; relay accepted this event recently"
