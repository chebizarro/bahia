package relaysidecar

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"
	"github.com/openagentsinc/bahia/internal/config"
)

type policy struct {
	servicePubkey nostr.PubKey
	hasServiceKey bool
	authorized    map[nostr.PubKey]struct{}
	now           func() nostr.Timestamp
}

func newPolicy(cfg config.NostrConfig) (*policy, error) {
	p := &policy{
		authorized: make(map[nostr.PubKey]struct{}, len(cfg.AuthorizedPubkeys)),
		now:        nostr.Now,
	}
	if servicePubkey, ok, err := deriveFiatjafPubkey(cfg.PrivateKey); err != nil {
		return nil, err
	} else if ok {
		p.servicePubkey = servicePubkey
		p.hasServiceKey = true
	}
	for _, raw := range cfg.AuthorizedPubkeys {
		pk, err := nostr.PubKeyFromHex(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("invalid nostr.authorized_pubkeys entry: %w", err)
		}
		p.authorized[pk] = struct{}{}
	}
	return p, nil
}

func (p *policy) acceptEvent(ctx context.Context, event nostr.Event) (bool, string) {
	if !event.CheckID() {
		return true, "invalid: id is computed incorrectly"
	}
	if !event.VerifySignature() {
		return true, "invalid: signature is invalid"
	}
	if event.CreatedAt > p.now()+nostr.Timestamp((10*time.Minute).Seconds()) {
		return true, "invalid: created_at too far in the future"
	}
	if p.now()-event.CreatedAt > nostr.Timestamp((365 * 24 * time.Hour).Seconds()) {
		return true, "invalid: created_at too far in the past"
	}

	switch {
	case isRequestKind(event.Kind):
		if _, ok := p.authorized[event.PubKey]; ok {
			return false, ""
		}
		return true, "restricted: request kind requires an authorized operator pubkey"
	case isBahiaProjectionKind(event.Kind):
		if p.hasServiceKey && event.PubKey == p.servicePubkey {
			return false, ""
		}
		return true, "restricted: Bahia projection kind requires the service pubkey"
	case isOpenInteropKind(event.Kind):
		return false, ""
	default:
		return true, fmt.Sprintf("blocked: event kind %d is not allowed on the Bahia sidecar", event.Kind)
	}
}

func (p *policy) acceptFilter(ctx context.Context, filter nostr.Filter) (bool, string) {
	if len(filter.Kinds) == 0 {
		return true, "blocked: Bahia sidecar reads must specify browser-safe kinds"
	}
	if filter.Search != "" {
		return true, "blocked: search queries are not enabled on the Bahia sidecar"
	}
	if filter.LimitZero {
		return false, ""
	}
	if filter.GetTheoreticalLimit() == 0 {
		return false, ""
	}
	for _, kind := range filter.Kinds {
		if isAuthorScopedReadableRequestKind(kind) {
			if !p.hasAuthorizedAuthors(filter.Authors) {
				return true, fmt.Sprintf("blocked: request kind %d reads must scope authors to authorized operator pubkeys", kind)
			}
			continue
		}
		if !isReadableKind(kind) {
			return true, fmt.Sprintf("blocked: event kind %d is not readable from the Bahia sidecar", kind)
		}
	}
	return false, ""
}

func deriveFiatjafPubkey(raw string) (nostr.PubKey, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nostr.ZeroPK, false, nil
	}
	if strings.HasPrefix(raw, "nsec") {
		prefix, value, err := nip19.Decode(raw)
		if err != nil {
			return nostr.ZeroPK, false, fmt.Errorf("decode nostr.private_key nsec: %w", err)
		}
		if prefix != "nsec" {
			return nostr.ZeroPK, false, fmt.Errorf("nostr.private_key bech32 prefix %q is not nsec", prefix)
		}
		sk, ok := value.(nostr.SecretKey)
		if !ok {
			return nostr.ZeroPK, false, fmt.Errorf("decode nostr.private_key nsec: unexpected value type %T", value)
		}
		return sk.Public(), true, nil
	}
	sk, err := nostr.SecretKeyFromHex(raw)
	if err != nil {
		return nostr.ZeroPK, false, fmt.Errorf("decode nostr.private_key hex: %w", err)
	}
	return sk.Public(), true, nil
}

func isRequestKind(kind nostr.Kind) bool {
	return (kind >= 5961 && kind <= 5989) || (kind >= 5991 && kind <= 5996)
}

func isBahiaProjectionKind(kind nostr.Kind) bool {
	return kind == 30002 ||
		kind == 30078 ||
		kind == 30079 ||
		kind == 31974 ||
		(kind >= 6961 && kind <= 6991) ||
		(kind >= 7961 && kind <= 7992) ||
		(kind >= 31961 && kind <= 31973) ||
		(kind >= 31000 && kind <= 31099)
}

func isOpenInteropKind(kind nostr.Kind) bool {
	switch kind {
	case 10100, 30100, 5101, 5102, 5401, 5402:
		return true
	default:
		return false
	}
}

func (p *policy) hasAuthorizedAuthors(authors []nostr.PubKey) bool {
	if len(authors) == 0 {
		return false
	}
	for _, pk := range authors {
		if _, ok := p.authorized[pk]; !ok {
			return false
		}
	}
	return true
}

func isAuthorScopedReadableRequestKind(kind nostr.Kind) bool {
	return isRequestKind(kind) && kind != 5980
}

func isReadableKind(kind nostr.Kind) bool {
	return isBahiaProjectionKind(kind) || isOpenInteropKind(kind)
}
