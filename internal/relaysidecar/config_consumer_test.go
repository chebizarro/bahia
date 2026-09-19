package relaysidecar

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

type stubConfigSigner struct{}

func (stubConfigSigner) Sign(context.Context, *nostr.Event) error { return nil }

type stubConfigPublisher struct{}

func (stubConfigPublisher) Publish(context.Context, nostr.Event) (int, error) { return 1, nil }

// membershipEvent builds a signed NIP-51 membership list carrying the supplied
// p tags verbatim, so tests can exercise tag shapes a publisher may legitimately
// emit (bare pubkey, pubkey plus relay hint, pubkey plus hint plus petname).
func membershipEvent(t *testing.T, sk nostr.SecretKey, pTags nostr.Tags) nostr.Event {
	t.Helper()
	content, err := json.Marshal(map[string]any{
		"service_id": "relay-sidecar-test",
		"scope":      "edge",
		"version":    1,
		"schema":     configMembershipSchema,
	})
	require.NoError(t, err)

	tags := nostr.Tags{
		{"service", "relay-sidecar-test"},
		{"scope", "edge"},
		{"version", "1"},
		{"schema", configMembershipSchema},
		{"d", "service:relay-sidecar-test:membership"},
	}
	tags = append(tags, pTags...)

	event := nostr.Event{
		Kind:      configListKind,
		CreatedAt: nostr.Now(),
		Tags:      tags,
		Content:   string(content),
	}
	require.NoError(t, event.Sign(sk))
	return event
}

// A p tag may carry a relay hint and a petname after the pubkey. Requiring an
// exact length silently dropped those members from the allowlist, shrinking an
// authorization set without any error.
func TestValidateAcceptsMembershipTagsWithRelayHints(t *testing.T) {
	sk := nostr.Generate()
	author := sk.Public().Hex()
	member := nostr.Generate().Public().Hex()
	hinted := nostr.Generate().Public().Hex()
	named := nostr.Generate().Public().Hex()

	consumer, err := NewConfigConsumer(ConfigConsumerConfig{
		ServiceID:      "relay-sidecar-test",
		Scope:          "edge",
		ProjectionPath: filepath.Join(t.TempDir(), "projection.json"),
		TrustedAuthors: []string{author},
		Signer:         stubConfigSigner{},
		Publisher:      stubConfigPublisher{},
		Apply:          func(ConfigProjection) error { return nil },
	})
	require.NoError(t, err)

	event := membershipEvent(t, sk, nostr.Tags{
		{"p", member},
		{"p", hinted, "wss://relay.example"},
		{"p", named, "wss://relay.example", "operator"},
	})

	projection, err := consumer.validate(event)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{member, hinted, named}, projection.AllowedPubkeys)
}
