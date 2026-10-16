package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/deletionguard"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// leakedKinds are the Bahia status kinds whose d tag carried the service
// private key: readiness (30360), identity (31410) and replay checkpoint
// (31411).
var leakedKinds = []nostr.Kind{
	nostr.Kind(kinds.BahiaReadinessStatus),
	nostr.Kind(kinds.BahiaIdentityDefinition),
	nostr.Kind(kinds.BahiaReplayCheckpoint),
}

func isLeakedKind(kind nostr.Kind) bool {
	for _, leaked := range leakedKinds {
		if kind == leaked {
			return true
		}
	}
	return false
}

// maxBatchSize bounds the e tags of one deletion request; relays cap tag
// counts and message sizes (NIP-11 max_event_tags, max_message_length).
const maxBatchSize = 500

// fingerprint is a domain-separated SHA-256 of a d value. The report keeps
// fingerprints, never values, so the guard can compare deletion tags against
// every scanned d value without the leaked key ever being written down. The
// leaked value is a 256-bit secret, so its hash cannot be inverted.
type fingerprint [sha256.Size]byte

func fingerprintOf(value string) fingerprint {
	return sha256.Sum256([]byte("bahia-leak-delete/d-value/v1\x00" + value))
}

type fingerprintSet map[fingerprint]struct{}

func (s fingerprintSet) add(value string) { s[fingerprintOf(value)] = struct{}{} }

func (s fingerprintSet) contains(value string) bool {
	_, ok := s[fingerprintOf(value)]
	return ok
}

func (s fingerprintSet) hexes() []string {
	out := make([]string, 0, len(s))
	for fp := range s {
		out = append(out, hex.EncodeToString(fp[:]))
	}
	return out
}

func parseFingerprints(values []string) (fingerprintSet, error) {
	set := make(fingerprintSet, len(values))
	for _, value := range values {
		raw, err := hex.DecodeString(value)
		if err != nil || len(raw) != sha256.Size {
			return nil, fmt.Errorf("invalid d fingerprint %q", value)
		}
		set[fingerprint(raw)] = struct{}{}
	}
	return set, nil
}

const redacted = "[redacted]"

// redact replaces every alphanumeric token of a relay-supplied string that is
// a scanned d value, and every token containing "nsec1". Relays write OK and
// CLOSED reasons; one that echoes a leaked event must not get its d value
// printed or recorded.
func (s fingerprintSet) redact(text string) string {
	var out strings.Builder
	start := -1
	flush := func(end int) {
		token := text[start:end]
		if s.contains(token) || strings.Contains(strings.ToLower(token), "nsec1") {
			token = redacted
		}
		out.WriteString(token)
		start = -1
	}
	for i, r := range text {
		alnum := r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r))
		switch {
		case alnum && start < 0:
			start = i
		case !alnum && start >= 0:
			flush(i)
			out.WriteRune(r)
		case !alnum:
			out.WriteRune(r)
		}
	}
	if start >= 0 {
		flush(len(text))
	}
	return out.String()
}

var (
	// errATag refuses an `a` (coordinate) reference: the coordinate
	// <kind>:<pubkey>:<d> of a leaked event contains the leaked key, so
	// publishing it would publish the key again.
	errATag = errors.New("refusing deletion with an a tag: the coordinate of a leaked event contains its d value (the leaked key)")
	// errDValue refuses any tag value equal to a scanned d value.
	errDValue = errors.New("refusing deletion: a tag value equals a scanned d value")
)

// deletionGuard is the hard gate every NIP-09 request passes before it is
// signed and again before every publish, including requests read back from a
// report. It is an allowlist: kind 5, authored by the leaked identity, empty
// content, at least one and at most maxBatchSize `e` tags naming events the
// scans found, `k` tags exactly for those events' kinds, and nothing else. No
// tag name or value may equal a scanned d value or contain "nsec1".
type deletionGuard struct {
	author nostr.PubKey
	// targets maps every scanned event id to its kind.
	targets   map[nostr.ID]nostr.Kind
	forbidden fingerprintSet
}

func (g deletionGuard) check(ev nostr.Event) error {
	for _, tag := range ev.Tags {
		if len(tag) > 0 && strings.EqualFold(strings.TrimSpace(tag[0]), "a") {
			return errATag
		}
		for _, field := range tag {
			if g.forbidden.contains(field) {
				return errDValue
			}
		}
	}
	allowed := make(map[nostr.Kind]struct{}, len(leakedKinds))
	for _, kind := range leakedKinds {
		allowed[kind] = struct{}{}
	}
	return deletionguard.Check(ev, g.author, g.targets, allowed)
}

func (g deletionGuard) checkSigned(ev nostr.Event) error {
	if err := g.check(ev); err != nil {
		return err
	}
	if !ev.CheckID() || !ev.VerifySignature() {
		return errors.New("signed deletion does not verify")
	}
	return nil
}
