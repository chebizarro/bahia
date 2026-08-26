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
	now           func() nostr.Timestamp
	admin         *adminPolicy
	servicePubkey string
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
	if event.CreatedAt > p.now()+nostr.Timestamp((10*time.Minute).Seconds()) {
		return true, "invalid: created_at too far in the future"
	}
	if p.now()-event.CreatedAt > nostr.Timestamp((365 * 24 * time.Hour).Seconds()) {
		return true, "invalid: created_at too far in the past"
	}
	if p.admin != nil && event.PubKey.Hex() != p.servicePubkey && !p.admin.admits(event.PubKey.Hex()) {
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
