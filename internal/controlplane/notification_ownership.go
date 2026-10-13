package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// NotificationOwnershipReader resolves a channel's scope from service-signed
// canonical state, never from the derived notification repository.
type NotificationOwnershipReader interface {
	OwnerOf(context.Context, uuid.UUID) (uuid.UUID, bool, error)
}

type localNotificationOwnershipReader struct {
	events interface {
		QueryEvents(nostr.Filter) iter.Seq[nostr.Event]
	}
	decryptor interface {
		DecryptServiceInner(context.Context, string) ([]byte, error)
	}
	author nostr.PubKey
}

func NewLocalNotificationOwnershipReader(events interface {
	QueryEvents(nostr.Filter) iter.Seq[nostr.Event]
}, decryptor interface {
	DecryptServiceInner(context.Context, string) ([]byte, error)
}, servicePubkey string) (NotificationOwnershipReader, error) {
	if events == nil || decryptor == nil {
		return nil, fmt.Errorf("notification ownership requires canonical state and decryptor")
	}
	author, err := nostr.PubKeyFromHex(strings.TrimSpace(servicePubkey))
	if err != nil || author == nostr.ZeroPK {
		return nil, fmt.Errorf("notification ownership service pubkey is invalid")
	}
	return &localNotificationOwnershipReader{events: events, decryptor: decryptor, author: author}, nil
}

func (r *localNotificationOwnershipReader) OwnerOf(ctx context.Context, id uuid.UUID) (uuid.UUID, bool, error) {
	if r == nil || r.events == nil || r.decryptor == nil || id == uuid.Nil {
		return uuid.Nil, false, fmt.Errorf("notification ownership lookup is unavailable")
	}
	d := id.String()
	topic := kinds.CPStateTopicNotificationChannelRegistry
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Authors: []nostr.PubKey{r.author}, Tags: nostr.TagMap{"d": {d}, "t": {topic}}, Limit: 1}
	for ev := range r.events.QueryEvents(filter) {
		if err := ctx.Err(); err != nil {
			return uuid.Nil, false, err
		}
		if ev.PubKey != r.author || !ev.CheckID() || !ev.VerifySignature() || tagValueNostr(ev.Tags, "d") != d || tagValueNostr(ev.Tags, "t") != topic || tagValueNostr(ev.Tags, "domain") != "notification" || tagValueNostr(ev.Tags, "schema") != kinds.CASControlStateSchema || tagValueNostr(ev.Tags, "legacy_kind") != fmt.Sprint(kinds.NotificationChannelRegistry) {
			return uuid.Nil, true, fmt.Errorf("notification channel has invalid canonical envelope")
		}
		scope, _, err := VersionFromEnvelope(ev.Content)
		if err != nil {
			return uuid.Nil, true, fmt.Errorf("notification channel has invalid canonical scope")
		}
		owner := uuid.Nil
		if scope != "fleet" {
			owner, err = uuid.Parse(scope)
			if err != nil || owner == uuid.Nil {
				return uuid.Nil, true, fmt.Errorf("notification channel has invalid canonical scope")
			}
		}
		if tagValueNostr(ev.Tags, "deleted") != "true" {
			plain, err := r.decryptor.DecryptServiceInner(ctx, ev.Content)
			if err != nil || len(plain) == 0 {
				return uuid.Nil, true, fmt.Errorf("notification channel has no valid service-owned state")
			}
			var channel struct {
				ID    uuid.UUID `json:"id"`
				OrgID uuid.UUID `json:"org_id"`
			}
			if json.Unmarshal(plain, &channel) != nil || channel.ID != id || channel.OrgID != owner {
				return uuid.Nil, true, fmt.Errorf("notification channel canonical owner is inconsistent")
			}
		}
		return owner, true, nil
	}
	return uuid.Nil, false, nil
}
