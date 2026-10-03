package relaysidecar

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/nostrutil"
)

// Admission bounds on created_at. NIP-11 advertises maxEventFutureSkew as
// created_at_upper_limit. maxEventAge applies only to the kinds
// nostrutil.AgeCapped reports, the rule the daemon's ValidateInboundEvent
// applies too, so it is not advertised: created_at_lower_limit has no per-kind
// form.
const (
	maxEventFutureSkew = 10 * time.Minute
	maxEventAge        = nostrutil.MaxEventAge
)

type policy struct {
	now           func() nostr.Timestamp
	admin         *adminPolicy
	servicePubkey string

	// intentAuthors is the set of pubkeys (hex) that may publish intent events
	// (kind 30900 + t=bahia-intent) through the sidecar. These pubkeys are NOT
	// added to the general admin allowlist: they may only write intent events.
	// The daemon updates this set via the NIP-86 setintentauthors method as its
	// TrustSet changes. Protected by intentAuthorsMu.
	intentAuthorsMu sync.RWMutex
	intentAuthors   map[string]bool
	configAuthors   map[string]bool
}

func newPolicy(cfg config.NostrConfig) (*policy, error) {
	servicePubkey, ok, err := deriveFiatjafPubkey(cfg.PrivateKey)
	if err != nil {
		return nil, err
	}
	value := ""
	if ok {
		value = servicePubkey.Hex()
	}
	return &policy{now: nostr.Now, servicePubkey: value}, nil
}

func (p *policy) acceptEvent(ctx context.Context, event nostr.Event) (bool, string) {
	if !event.CheckID() {
		return true, "invalid: id is computed incorrectly"
	}
	if !event.VerifySignature() {
		return true, "invalid: signature is invalid"
	}
	if event.CreatedAt > p.now()+nostr.Timestamp(maxEventFutureSkew.Seconds()) {
		return true, "invalid: created_at too far in the future"
	}
	if nostrutil.AgeCapped(event.Kind) && p.now()-event.CreatedAt > nostr.Timestamp(maxEventAge.Seconds()) {
		return true, "invalid: created_at too far in the past (regular and ephemeral events older than one year are refused)"
	}
	if expired(event, p.now()) {
		return true, "invalid: event has expired (NIP-40)"
	}
	if p.admin != nil && event.PubKey.Hex() != p.servicePubkey && !p.admin.admits(event.PubKey.Hex()) {
		// Intent write policy (§7.1): kind 30900 + t=bahia-intent events from
		// pubkeys in the intentAuthors set are admitted even when not on the
		// general admin allowlist. The daemon updates intentAuthors via the
		// NIP-86 setintentauthors method as its TrustSet changes. Non-intent
		// events from intent authors remain blocked.
		if isIntentEvent(event) && p.admitsIntentAuthor(event.PubKey.Hex()) {
			return false, ""
		}
		if (event.Kind == configListKind || event.Kind == configPolicyKind) && p.configAuthors[event.PubKey.Hex()] {
			return false, ""
		}
		return true, "blocked: pubkey is not admitted by the persisted relay policy"
	}

	return false, ""
}

func (p *policy) acceptFilter(ctx context.Context, filter nostr.Filter) (bool, string) {
	if filter.Search != "" {
		return true, "blocked: search queries are not enabled on the Bahia sidecar"
	}
	return false, ""
}

func parseFiatjafSecret(raw string) (nostr.SecretKey, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nostr.SecretKey{}, false, nil
	}
	if strings.HasPrefix(raw, "nsec") {
		prefix, value, err := nip19.Decode(raw)
		if err != nil {
			return nostr.SecretKey{}, false, fmt.Errorf("decode nostr.private_key nsec: %w", err)
		}
		if prefix != "nsec" {
			return nostr.SecretKey{}, false, fmt.Errorf("nostr.private_key bech32 prefix %q is not nsec", prefix)
		}
		sk, ok := value.(nostr.SecretKey)
		if !ok {
			return nostr.SecretKey{}, false, fmt.Errorf("decode nostr.private_key nsec: unexpected value type %T", value)
		}
		return sk, true, nil
	}
	sk, err := nostr.SecretKeyFromHex(raw)
	if err != nil {
		return nostr.SecretKey{}, false, fmt.Errorf("decode nostr.private_key hex: %w", err)
	}
	return sk, true, nil
}

func deriveFiatjafPubkey(raw string) (nostr.PubKey, bool, error) {
	sk, ok, err := parseFiatjafSecret(raw)
	if err != nil || !ok {
		return nostr.ZeroPK, ok, err
	}
	return sk.Public(), true, nil
}

// SetIntentAuthors replaces the set of pubkeys that may publish intent events.
// The daemon calls this via the NIP-86 setintentauthors method whenever its
// TrustSet changes. These pubkeys gain write access for kind 30900 +
// t=bahia-intent only; other event kinds remain gated by the admin allowlist.
func (p *policy) SetIntentAuthors(pubkeys []string) {
	p.intentAuthorsMu.Lock()
	defer p.intentAuthorsMu.Unlock()
	set := make(map[string]bool, len(pubkeys))
	for _, pk := range pubkeys {
		if pk != "" {
			set[pk] = true
		}
	}
	p.intentAuthors = set
}

// admitsIntentAuthor reports whether pubkey is in the intent authors set.
func (p *policy) admitsIntentAuthor(pubkey string) bool {
	p.intentAuthorsMu.RLock()
	defer p.intentAuthorsMu.RUnlock()
	return p.intentAuthors[pubkey]
}

// isIntentEvent reports whether ev is a kind-30900 operator intent event
// carrying the t=bahia-intent tag.
func isIntentEvent(ev nostr.Event) bool {
	if ev.Kind != 30900 {
		return false
	}
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == "t" && tag[1] == "bahia-intent" {
			return true
		}
	}
	return false
}
