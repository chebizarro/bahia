package nip55l

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/godbus/dbus/v5"
)

// Sentinel errors for the org.nostr.Signer.Error.* names (nip55l 0.7.0) and
// for transport and verification failures. Errors that come from the signer
// are *SignerError values whose Unwrap yields one of these (or
// errors.ErrUnsupported), so callers can use errors.Is.
var (
	// ErrApprovalDenied: the request was not approved (user, remembered
	// decision, or caller disconnect). A signer that ignored the typed-error
	// opt-in also reports timeouts, a missing UI and identity changes this way.
	ErrApprovalDenied = errors.New("nip55l: approval denied")
	// ErrApprovalTimedOut: a parked request expired unanswered (0.5.0).
	ErrApprovalTimedOut = errors.New("nip55l: approval timed out")
	// ErrNoApprovalAgent: a prompt was needed but no approval UI could be
	// reached (0.5.0).
	ErrNoApprovalAgent = errors.New("nip55l: no approval agent")
	// ErrIdentityChanged: approved, but the selector no longer resolves to
	// the approved npub (0.5.0).
	ErrIdentityChanged = errors.New("nip55l: signer identity changed")
	// ErrRateLimited: too many requests, or too many parked requests.
	ErrRateLimited = errors.New("nip55l: rate limited")
	// ErrNoKeyConfigured: the identity selector resolves to no stored key.
	ErrNoKeyConfigured = errors.New("nip55l: no key configured for identity")
	// ErrInvalidInput: the signer rejected an event, peer key or selector.
	ErrInvalidInput = errors.New("nip55l: invalid input")
	// ErrPermissionDenied: the caller may not use the method.
	ErrPermissionDenied = errors.New("nip55l: permission denied")
	// ErrNotFound: GetRelays found no configured relays.
	ErrNotFound = errors.New("nip55l: not found")
	// ErrInvalidConfig: GetRelays found a malformed relays.conf.
	ErrInvalidConfig = errors.New("nip55l: invalid signer config")
	// ErrInternal: signer or backend failure.
	ErrInternal = errors.New("nip55l: signer internal error")
	// ErrUnknownSignerError: an error name this client does not know, for
	// example from a newer signer revision.
	ErrUnknownSignerError = errors.New("nip55l: unknown signer error")

	// ErrSignerUnavailable: no signer owns org.nostr.Signer, it cannot be
	// activated, or the bus connection is gone.
	ErrSignerUnavailable = errors.New("nip55l: signer unavailable")
	// ErrTimeout: no reply within Config.CallTimeout, or the bus reported
	// NoReply. The signer may still complete the request; this package never
	// retries it.
	ErrTimeout = errors.New("nip55l: signer call timed out")
	// ErrBadReply: the reply failed local verification (wrong pubkey,
	// altered event, malformed payload).
	ErrBadReply = errors.New("nip55l: signer reply failed verification")
	// ErrIdentityNotFound: New could not confirm the signer holds
	// Config.ServicePubkey.
	ErrIdentityNotFound = errors.New("nip55l: signer does not hold the service identity")
	// ErrClosed: the Keyer was closed.
	ErrClosed = errors.New("nip55l: keyer closed")
)

const signerErrorPrefix = signerInterface + ".Error."

var signerErrorKinds = map[string]error{
	"ApprovalDenied":   ErrApprovalDenied,
	"ApprovalTimedOut": ErrApprovalTimedOut,
	"NoApprovalAgent":  ErrNoApprovalAgent,
	"IdentityChanged":  ErrIdentityChanged,
	"RateLimited":      ErrRateLimited,
	"NoKeyConfigured":  ErrNoKeyConfigured,
	"InvalidInput":     ErrInvalidInput,
	"PermissionDenied": ErrPermissionDenied,
	"NotFound":         ErrNotFound,
	"InvalidConfig":    ErrInvalidConfig,
	"Internal":         ErrInternal,
}

// Standard D-Bus error names this package maps.
const (
	dbusErrUnknownMethod    = "org.freedesktop.DBus.Error.UnknownMethod"
	dbusErrUnknownObject    = "org.freedesktop.DBus.Error.UnknownObject"
	dbusErrUnknownInterface = "org.freedesktop.DBus.Error.UnknownInterface"
	dbusErrServiceUnknown   = "org.freedesktop.DBus.Error.ServiceUnknown"
	dbusErrNameHasNoOwner   = "org.freedesktop.DBus.Error.NameHasNoOwner"
	dbusErrSpawnPrefix      = "org.freedesktop.DBus.Error.Spawn."
	dbusErrNoReply          = "org.freedesktop.DBus.Error.NoReply"
	dbusErrTimeout          = "org.freedesktop.DBus.Error.Timeout"
	dbusErrTimedOut         = "org.freedesktop.DBus.Error.TimedOut"
	dbusErrDisconnected     = "org.freedesktop.DBus.Error.Disconnected"
)

// maxSignerMessage bounds the informational signer message kept in errors.
const maxSignerMessage = 256

// SignerError is a failed or rejected org.nostr.Signer call. Kind is one of
// the package sentinels or errors.ErrUnsupported. Name is the D-Bus error
// name, empty for locally detected failures. Message is informational (the
// spec says messages are not part of the contract) and truncated.
type SignerError struct {
	Method  string
	Name    string
	Message string
	Kind    error
}

func (e *SignerError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "nip55l %s: %v", e.Method, e.Kind)
	switch {
	case e.Name != "" && e.Message != "":
		fmt.Fprintf(&b, " (%s: %s)", e.Name, e.Message)
	case e.Name != "":
		fmt.Fprintf(&b, " (%s)", e.Name)
	case e.Message != "":
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	return b.String()
}

func (e *SignerError) Unwrap() error { return e.Kind }

// mapCallError converts a godbus call error into this package's vocabulary.
// parent is the caller's context: its cancellation is reported as the
// context error, distinct from the Keyer's own CallTimeout.
func mapCallError(parent context.Context, method string, err error) error {
	if err == nil {
		return nil
	}
	if perr := parent.Err(); perr != nil {
		return fmt.Errorf("nip55l %s: %w", method, perr)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("nip55l %s: %w: %w", method, ErrTimeout, err)
	}
	if errors.Is(err, dbus.ErrClosed) {
		return &SignerError{Method: method, Kind: ErrSignerUnavailable, Message: err.Error()}
	}
	name, body, ok := dbusErrorParts(err)
	if !ok {
		return &SignerError{Method: method, Kind: ErrSignerUnavailable, Message: err.Error()}
	}
	se := &SignerError{Method: method, Name: name, Message: firstStringBody(body)}
	switch {
	case strings.HasPrefix(name, signerErrorPrefix):
		if kind, known := signerErrorKinds[strings.TrimPrefix(name, signerErrorPrefix)]; known {
			se.Kind = kind
		} else {
			se.Kind = ErrUnknownSignerError
		}
	case name == dbusErrUnknownMethod:
		se.Kind = errors.ErrUnsupported
	case name == dbusErrServiceUnknown, name == dbusErrNameHasNoOwner,
		name == dbusErrUnknownObject, name == dbusErrUnknownInterface,
		name == dbusErrDisconnected, strings.HasPrefix(name, dbusErrSpawnPrefix):
		se.Kind = ErrSignerUnavailable
	case name == dbusErrNoReply, name == dbusErrTimeout, name == dbusErrTimedOut:
		se.Kind = ErrTimeout
	default:
		se.Kind = ErrUnknownSignerError
	}
	return se
}

func dbusErrorParts(err error) (string, []any, bool) {
	var v dbus.Error
	if errors.As(err, &v) {
		return v.Name, v.Body, true
	}
	var p *dbus.Error
	if errors.As(err, &p) && p != nil {
		return p.Name, p.Body, true
	}
	return "", nil, false
}

func firstStringBody(body []any) string {
	if len(body) == 0 {
		return ""
	}
	s, ok := body[0].(string)
	if !ok {
		return ""
	}
	if len(s) > maxSignerMessage {
		cut := maxSignerMessage
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "..."
	}
	return s
}

func isUnknownMethod(err error) bool {
	var se *SignerError
	return errors.As(err, &se) && se.Name == dbusErrUnknownMethod
}

func badReply(method, format string, args ...any) error {
	return &SignerError{Method: method, Kind: ErrBadReply, Message: fmt.Sprintf(format, args...)}
}
