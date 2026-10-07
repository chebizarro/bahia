package relaysidecar

import (
	"context"
	"encoding/json"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

// desiredWithTags builds a signed membership desired event at createdAt.
func desiredWithTags(t *testing.T, secret nostr.SecretKey, version int, createdAt nostr.Timestamp, extra ...nostr.Tag) nostr.Event {
	t.Helper()
	content, err := json.Marshal(map[string]any{
		"service_id": "relay-sidecar-test", "scope": "prod", "version": version,
		"schema": configMembershipSchema,
	})
	require.NoError(t, err)
	desired := nostr.Event{
		Kind: configListKind, CreatedAt: createdAt, Content: string(content),
		Tags: append(nostr.Tags{
			{"d", "service:relay-sidecar-test:membership"}, {"service", "relay-sidecar-test"},
			{"scope", "prod"}, {"version", strconv.Itoa(version)}, {"schema", configMembershipSchema},
			{"p", secret.Public().Hex()},
		}, extra...),
	}
	require.NoError(t, desired.Sign(secret))
	return desired
}

func desiredEventIDForTest(c *ConfigConsumer, author string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	desired := c.state.Desired[author+"\x00"+c.serviceID+"\x00"+c.scope+"\x00membership"]
	return desired.EventID, desired.Withdrawn
}

// statusRecorder captures published config statuses and signals each one.
func statusRecorder(t *testing.T, server *Server) chan nostr.Event {
	t.Helper()
	statuses := make(chan nostr.Event, 64)
	server.consumer.publisher = configStatusPublisherFunc(func(_ context.Context, event nostr.Event) (int, error) {
		statuses <- event
		return 1, nil
	})
	return statuses
}

func awaitStatus(t *testing.T, ctx context.Context, statuses chan nostr.Event, status, eventID string) {
	t.Helper()
	for {
		select {
		case event := <-statuses:
			if statusNameForTest(t, event) == status && event.Tags.Find("e")[1] == eventID {
				return
			}
		case <-ctx.Done():
			t.Fatalf("no %q status for %s", status, eventID)
		}
	}
}

// two desired events claiming one version resolve like NIP-01
// replacement (lowest id on equal created_at), whatever order they arrive in.
func TestConfigConsumerEqualVersionResolvesByLowestID(t *testing.T) {
	secret := nostr.SecretKey{1}
	a := desiredWithTags(t, secret, 4, 1790200004, nostr.Tag{"nonce", "a"})
	b := desiredWithTags(t, secret, 4, 1790200004, nostr.Tag{"nonce", "b"})
	low, high := a, b
	if low.ID.Hex() > high.ID.Hex() {
		low, high = high, low
	}
	for name, order := range map[string][]nostr.Event{"low-first": {low, high}, "high-first": {high, low}} {
		t.Run(name, func(t *testing.T) {
			server, _ := configStatusServerForTest(t)
			for _, event := range order {
				_ = server.consumer.Handle(t.Context(), event)
			}
			got, _ := desiredEventIDForTest(server.consumer, secret.Public().Hex())
			require.Equal(t, low.ID.Hex(), got)
		})
	}
}

func TestConfigConsumerRejectsExpiredDesiredEvent(t *testing.T) {
	server, secret := configStatusServerForTest(t)
	expired := desiredWithTags(t, secret, 1, 1790199000, nostr.Tag{"expiration", "1790200000"})
	require.ErrorContains(t, server.consumer.Handle(t.Context(), expired), "expired")
	got, _ := desiredEventIDForTest(server.consumer, secret.Public().Hex())
	require.Empty(t, got)
}

// a NIP-09 deletion of the desired event withdraws it, its pending
// activation never runs, and the deleted event cannot come back.
func TestSidecarConfigDeletionWithdrawsDesiredWithoutResurrection(t *testing.T) {
	for _, ref := range []string{"e", "a"} {
		t.Run(ref, func(t *testing.T) {
			server, secret := configStatusServerForTest(t)
			statuses := statusRecorder(t, server)
			desired := desiredWithTags(t, secret, 1, 1790200001)
			_, err := server.Relay().AddEvent(t.Context(), desired)
			require.NoError(t, err)
			ctx := startConfigWorkersForTest(t, server)
			awaitStatus(t, ctx, statuses, "applied", desired.ID.Hex())

			target := desired.ID.Hex()
			if ref == "a" {
				target = replaceableKey(desired)
			}
			deletion := nostr.Event{Kind: nostr.KindDeletion, CreatedAt: 1790200100, Tags: nostr.Tags{{ref, target}, {"k", strconv.Itoa(int(configListKind))}}}
			require.NoError(t, deletion.Sign(secret))
			_, err = server.Relay().AddEvent(t.Context(), deletion)
			require.NoError(t, err)
			awaitStatus(t, ctx, statuses, "withdrawn", desired.ID.Hex())
			_, withdrawn := desiredEventIDForTest(server.consumer, secret.Public().Hex())
			require.True(t, withdrawn)

			// Whether or not the store refuses a re-published copy (it cannot for
			// `a` coordinates over the eventstore's 100-byte tag index limit),
			// the consumer keeps the withdrawn version floor.
			require.ErrorContains(t, server.consumer.Handle(t.Context(), desired), "does not advance", "the consumer must not re-accept a withdrawn event")
			_, withdrawn = desiredEventIDForTest(server.consumer, secret.Public().Hex())
			require.True(t, withdrawn)
		})
	}
}

// a desired event is withdrawn when its NIP-40 expiration passes, at
// that moment rather than at the next retention sweep.
func TestSidecarConfigExpirationWithdrawsDesired(t *testing.T) {
	server, secret := configStatusServerForTest(t)
	statuses := statusRecorder(t, server)
	start := time.Now().Truncate(time.Second)
	var clock atomic.Int64
	clock.Store(start.Unix())
	server.consumer.now = func() time.Time { return time.Unix(clock.Load(), 0) }
	scheduled := make(chan func(), 4)
	server.configAfter = func(_ time.Duration, fire func()) { scheduled <- fire }

	expiresAt := start.Add(time.Hour)
	desired := desiredWithTags(t, secret, 1, nostr.Timestamp(start.Unix()-10), nostr.Tag{"expiration", strconv.FormatInt(expiresAt.Unix(), 10)})
	_, err := server.Relay().AddEvent(t.Context(), desired)
	require.NoError(t, err)
	ctx := startConfigWorkersForTest(t, server)
	awaitStatus(t, ctx, statuses, "applied", desired.ID.Hex())

	var fire func()
	select {
	case fire = <-scheduled:
	case <-ctx.Done():
		t.Fatal("no expiry re-read was scheduled")
	}
	clock.Store(expiresAt.Unix())
	fire()
	awaitStatus(t, ctx, statuses, "withdrawn", desired.ID.Hex())
}
