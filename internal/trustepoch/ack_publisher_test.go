package trustepoch

import (
	"context"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

func TestComponentAcknowledgmentPublisherIsIdempotentAndAuthorityBound(t *testing.T) {
	epoch, body, root, old, newKey := fixture(t)
	component := nostr.MustSecretKeyFromHex("4444444444444444444444444444444444444444444444444444444444444444")
	resolved := Resolution{ServiceID: "deployment-a", Pubkey: body.NewPubkey,
		Epoch: body.Epoch, Record: &body, EventID: epoch.ID.Hex()}
	var stored []nostr.Event
	publications := 0
	publisher := AckPublisher{ConsumerID: "dns", Authority: component.Public().Hex(),
		OperatorRoot: root.Public().Hex(),
		Existing:     func(_ context.Context, _ nostr.Filter) ([]nostr.Event, error) { return stored, nil },
		Sign:         func(_ context.Context, ev *nostr.Event) error { return ev.Sign(component) },
		Publish: func(_ context.Context, ev nostr.Event) error {
			publications++
			stored = append(stored, ev)
			return nil
		}}
	require.NoError(t, publisher.Acknowledge(t.Context(), resolved))
	require.NoError(t, publisher.Acknowledge(t.Context(), resolved))
	require.Equal(t, 1, publications)
	require.NoError(t, VerifyAcknowledgmentAuthority(stored[0], resolved, "dns", component.Public().Hex(), false))
	publisher.Authority = old.Public().Hex()
	require.Error(t, publisher.Acknowledge(t.Context(), resolved))
	publisher.Authority = newKey.Public().Hex()
	require.Error(t, publisher.Acknowledge(t.Context(), resolved))
	publisher.Authority = root.Public().Hex()
	require.Error(t, publisher.Acknowledge(t.Context(), resolved))
}
