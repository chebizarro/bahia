package relaysidecar

import (
	"context"
	"net/http/httptest"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestIsIntentEvent(t *testing.T) {
	tests := []struct {
		name string
		ev   nostr.Event
		want bool
	}{
		{
			name: "kind 30900 with bahia-intent tag",
			ev: nostr.Event{
				Kind: 30900,
				Tags: nostr.Tags{
					{"d", "svc-123"},
					{"t", "bahia-intent"},
					{"domain", "service"},
				},
			},
			want: true,
		},
		{
			name: "kind 30900 without bahia-intent tag",
			ev: nostr.Event{
				Kind: 30900,
				Tags: nostr.Tags{
					{"d", "svc-123"},
					{"domain", "service"},
				},
			},
			want: false,
		},
		{
			name: "kind 30900 with different t tag",
			ev: nostr.Event{
				Kind: 30900,
				Tags: nostr.Tags{
					{"d", "svc-123"},
					{"t", "other-tag"},
				},
			},
			want: false,
		},
		{
			name: "wrong kind with bahia-intent tag",
			ev: nostr.Event{
				Kind: 1,
				Tags: nostr.Tags{
					{"t", "bahia-intent"},
				},
			},
			want: false,
		},
		{
			name: "kind 30315 is not an intent",
			ev: nostr.Event{
				Kind: 30315,
				Tags: nostr.Tags{
					{"t", "bahia-intent"},
				},
			},
			want: false,
		},
		{
			name: "empty tags",
			ev: nostr.Event{
				Kind: 30900,
				Tags: nostr.Tags{},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isIntentEvent(tt.ev))
		})
	}
}

// signedIntentEvent creates a signed kind-30900 intent event with the
// required t=bahia-intent tag.
func signedIntentEvent(t *testing.T, sk nostr.SecretKey) nostr.Event {
	t.Helper()
	ev := nostr.Event{
		Kind:      30900,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", "svc-intent-test"},
			{"t", "bahia-intent"},
			{"domain", "service"},
			{"op", "update"},
			{"org", "00000000-0000-0000-0000-000000000001"},
			{"intent_id", "test-intent-001"},
		},
		Content: `{"name":"test-service"}`,
	}
	require.NoError(t, ev.Sign(sk))
	return ev
}

// startSidecarWithAllowlist creates a sidecar whose admin policy restricts
// writes to the given admin pubkeys. This is necessary for intent author
// tests: without an allowlist, admits() returns true for everyone and the
// intent authors set is never exercised.
func startSidecarWithAllowlist(t *testing.T, adminPubkeys []string) (*Server, string) {
	t.Helper()
	cfg := sidecarTestConfig(t)
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334"
	cfg.Sidecar.AdministratorPubkeys = adminPubkeys
	server, err := New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close()) })
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	return server, httpServer.URL
}

// TestSidecarIntentFromSetMemberAccepted verifies that a kind-30900 intent
// event from a pubkey in the intent authors set is admitted even when the
// pubkey is not on the admin allowlist.
func TestSidecarIntentFromSetMemberAccepted(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), fanoutTestTimeout)
	defer cancel()

	admin := nostr.Generate()
	server, relayURL := startSidecarWithAllowlist(t, []string{admin.Public().Hex()})

	member := nostr.Generate()
	server.SetIntentAuthors([]string{member.Public().Hex()})

	// Allow the admin so the allowlist is non-empty (restricting writes).
	require.NoError(t, server.policy.mutatePubkey(admin.Public(), "admin", true))

	client := dialRawRelay(t, ctx, relayURL)
	intent := signedIntentEvent(t, member)
	ok := client.publishFrame(intent)
	require.True(t, ok.ok, "intent from set member should be accepted, got: %s", ok.reason)
}

// TestSidecarNonIntentFromSetMemberBlocked verifies that a non-intent event
// (e.g. kind 1 note) from a pubkey in the intent authors set is still blocked
// by the admin allowlist. Intent authors only gain write access for intents.
func TestSidecarNonIntentFromSetMemberBlocked(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), fanoutTestTimeout)
	defer cancel()

	admin := nostr.Generate()
	server, relayURL := startSidecarWithAllowlist(t, []string{admin.Public().Hex()})

	member := nostr.Generate()
	server.SetIntentAuthors([]string{member.Public().Hex()})
	require.NoError(t, server.policy.mutatePubkey(admin.Public(), "admin", true))

	client := dialRawRelay(t, ctx, relayURL)
	note := nostr.Event{
		Kind:      1,
		CreatedAt: nostr.Now(),
		Content:   "should be blocked",
	}
	require.NoError(t, note.Sign(member))
	ok := client.publishFrame(note)
	require.False(t, ok.ok, "non-intent event from intent author should be blocked")
	require.Contains(t, ok.reason, "blocked:")
}

// TestSidecarIntentFromNonMemberBlocked verifies that a kind-30900 intent
// event from a pubkey NOT in the intent authors set is blocked.
func TestSidecarIntentFromNonMemberBlocked(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), fanoutTestTimeout)
	defer cancel()

	admin := nostr.Generate()
	server, relayURL := startSidecarWithAllowlist(t, []string{admin.Public().Hex()})

	outsider := nostr.Generate()
	other := nostr.Generate()
	server.SetIntentAuthors([]string{other.Public().Hex()})
	require.NoError(t, server.policy.mutatePubkey(admin.Public(), "admin", true))

	client := dialRawRelay(t, ctx, relayURL)
	intent := signedIntentEvent(t, outsider)
	ok := client.publishFrame(intent)
	require.False(t, ok.ok, "intent from non-member should be blocked")
	require.Contains(t, ok.reason, "blocked:")
}

// TestSidecarIntentAuthorsUpdateTakesEffect verifies that updating the intent
// authors set via SetIntentAuthors takes effect for subsequent publishes.
func TestSidecarIntentAuthorsUpdateTakesEffect(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), fanoutTestTimeout)
	defer cancel()

	admin := nostr.Generate()
	server, relayURL := startSidecarWithAllowlist(t, []string{admin.Public().Hex()})
	require.NoError(t, server.policy.mutatePubkey(admin.Public(), "admin", true))

	member := nostr.Generate()
	client := dialRawRelay(t, ctx, relayURL)

	// Initially no intent authors: intent should be blocked.
	intent1 := signedIntentEvent(t, member)
	ok := client.publishFrame(intent1)
	require.False(t, ok.ok, "intent should be blocked before set update")

	// Daemon updates intent authors to include member.
	server.SetIntentAuthors([]string{member.Public().Hex()})

	// Now the member's intent should be accepted.
	intent2 := nostr.Event{
		Kind:      30900,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", "svc-intent-test-2"},
			{"t", "bahia-intent"},
			{"domain", "service"},
			{"op", "create"},
			{"org", "00000000-0000-0000-0000-000000000001"},
			{"intent_id", "test-intent-002"},
		},
		Content: `{"name":"test-service-2"}`,
	}
	require.NoError(t, intent2.Sign(member))
	ok = client.publishFrame(intent2)
	require.True(t, ok.ok, "intent should be accepted after set update, got: %s", ok.reason)

	// Remove member from the set: further intents should be blocked.
	server.SetIntentAuthors(nil)
	intent3 := nostr.Event{
		Kind:      30900,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", "svc-intent-test-3"},
			{"t", "bahia-intent"},
			{"domain", "service"},
			{"op", "delete"},
			{"org", "00000000-0000-0000-0000-000000000001"},
			{"intent_id", "test-intent-003"},
		},
		Content: `{}`,
	}
	require.NoError(t, intent3.Sign(member))
	ok = client.publishFrame(intent3)
	require.False(t, ok.ok, "intent should be blocked after member removed from set")
}
