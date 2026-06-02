package relaysidecar

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/kinds"
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
	case event.Kind == nostr.Kind(kinds.ContextVMMessage):
		if p.isAuthorizedOrServicePubkey(event.PubKey) {
			return false, ""
		}
		return true, "restricted: ContextVM message kind requires the service pubkey or an authorized operator pubkey"
	case event.Kind == nostr.Kind(kinds.ContextVMGiftWrap) || event.Kind == nostr.Kind(kinds.ContextVMEphemeralGiftWrap):
		if p.hasAllowedPTag(event.Tags) {
			return false, ""
		}
		return true, "restricted: ContextVM gift-wrap kind requires a p tag addressed to the service pubkey or an authorized operator pubkey"
	case event.Kind == nostr.Kind(kinds.EncryptedRequest):
		return true, "restricted: legacy encrypted request kind is migration-only"
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
		if isContextVMTransportKind(kind) {
			if !p.hasScopedContextVMRead(filter) {
				return true, fmt.Sprintf("blocked: ContextVM kind %d reads must scope authors or #p to the service pubkey or authorized operator pubkeys", kind)
			}
			continue
		}
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

func isBahiaProjectionKind(kind nostr.Kind) bool {
	return kinds.IsBahiaProjectionKind(int(kind))
}

func isOpenInteropKind(kind nostr.Kind) bool {
	return kinds.IsOpenInteropKind(int(kind))
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

func (p *policy) hasScopedContextVMRead(filter nostr.Filter) bool {
	if p.hasAllowedAuthors(filter.Authors) {
		return true
	}
	return p.hasAllowedPFilter(filter)
}

func (p *policy) hasAllowedAuthors(authors []nostr.PubKey) bool {
	if len(authors) == 0 {
		return false
	}
	for _, pk := range authors {
		if !p.isAuthorizedOrServicePubkey(pk) {
			return false
		}
	}
	return true
}

func (p *policy) hasAllowedPFilter(filter nostr.Filter) bool {
	values := filter.Tags["p"]
	if len(values) == 0 {
		return false
	}
	for _, raw := range values {
		if !p.isAllowedPubkeyHex(raw) {
			return false
		}
	}
	return true
}

func (p *policy) hasAllowedPTag(tags nostr.Tags) bool {
	for _, tag := range tags {
		if len(tag) < 2 || tag[0] != "p" {
			continue
		}
		if p.isAllowedPubkeyHex(tag[1]) {
			return true
		}
	}
	return false
}

func (p *policy) isAllowedPubkeyHex(raw string) bool {
	pk, err := nostr.PubKeyFromHex(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return p.isAuthorizedOrServicePubkey(pk)
}

func (p *policy) isAuthorizedOrServicePubkey(pk nostr.PubKey) bool {
	if _, ok := p.authorized[pk]; ok {
		return true
	}
	return p.hasServiceKey && pk == p.servicePubkey
}

func isContextVMTransportKind(kind nostr.Kind) bool {
	switch int(kind) {
	case kinds.ContextVMMessage, kinds.ContextVMGiftWrap, kinds.ContextVMEphemeralGiftWrap:
		return true
	default:
		return false
	}
}

func isAuthorScopedReadableRequestKind(kind nostr.Kind) bool {
	return kinds.IsAuthorScopedReadableRequestKind(int(kind))
}

func isReadableKind(kind nostr.Kind) bool {
	return kinds.IsReadableKind(int(kind))
}
