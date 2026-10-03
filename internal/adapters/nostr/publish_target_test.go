package nostr

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/repository/repositorytest"
	"github.com/stretchr/testify/require"
)

// sharedOutbox wraps one repository in a signalingOutbox per runner so each
// runner's discovery passes are observed separately while both read the same
// rows.
func sharedOutbox(repo *repositorytest.InMemoryNostrEventRepository) *signalingOutbox {
	outbox := newSignalingOutbox()
	outbox.InMemoryNostrEventRepository = repo
	return outbox
}

// discoveryPass nudges a runner and waits until it has started and then
// finished a discovery pass: the second listing can only begin once the first
// pass (including any delivery rounds it started) has returned.
func discoveryPass(t *testing.T, publisher *Publisher, outbox *signalingOutbox) {
	t.Helper()
	for range 2 {
		publisher.nudge()
		receive(t, outbox.listed, "runner discovery pass")
	}
}

// A control-plane row (docs, SBOM or config-fabric) with one control-plane relay
// down is retried by the control-plane runner to that relay only, until it
// accepts, and is never adopted by the interop runner or sent to interop
// relays.
func TestControlPlaneRowRetriedToItsDownRelayNotToInteropRelays(t *testing.T) {
	operator := gonostr.Generate()
	cases := []struct {
		name    string
		publish func(t *testing.T, publisher *Publisher, repo repository.NostrEventRepository) string
	}{
		{
			// docs.NostrDocsPublisher calls PublishSignedEvent.
			name: "docs",
			publish: func(t *testing.T, publisher *Publisher, _ repository.NostrEventRepository) string {
				ev := testSignedEvent("docs-topic")
				require.NoError(t, publisher.PublishSignedEvent(context.Background(), ev))
				return ev.ID.Hex()
			},
		},
		{
			// The SBOM orchestrator calls PublishSignedEventWithResults.
			name: "sbom",
			publish: func(t *testing.T, publisher *Publisher, _ repository.NostrEventRepository) string {
				ev := testSignedEvent("sbom-reference")
				_, err := publisher.PublishSignedEventWithResults(context.Background(), ev)
				require.NoError(t, err)
				return ev.ID.Hex()
			},
		},
		{
			// config-fabric records its operator-signed desired row for the
			// control-plane target, then hands the event over unchanged.
			name: "config_fabric",
			publish: func(t *testing.T, publisher *Publisher, repo repository.NostrEventRepository) string {
				ev := testSignedEvent("config-desired")
				require.NoError(t, ev.Sign(operator))
				rec := nostrEventRecordFromEvent(*ev, "config-fabric.desired", nil)
				rec.PublishState = repository.NostrPublishStatePending
				rec.PublishTarget = repository.NostrPublishTargetControlPlane
				_, err := repo.Record(context.Background(), rec)
				require.NoError(t, err)
				_, err = publisher.PublishPresignedEvent(context.Background(), *ev, "config-fabric.desired")
				require.NoError(t, err)
				return ev.ID.Hex()
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := repositorytest.NewInMemoryNostrEventRepository()
			cpOutbox := sharedOutbox(repo)
			interopOutbox := sharedOutbox(repo)

			interopRelays := newScriptedRelays(map[string][]PublishResult{relayC: {{Accepted: true}}})
			interop := newDeliveryTestPublisher(t, interopOutbox, interopRelays, 0, relayC)

			cpRelays := newScriptedRelays(map[string][]PublishResult{
				relayA: {{Accepted: true}},
				relayB: {
					{Error: errors.New("connection refused")},
					{Error: errors.New("connection refused")},
					{Accepted: true},
				},
			})
			controlPlane := newDeliveryTestPublisher(t, cpOutbox, cpRelays, 0, relayA, relayB)
			WithPublishTarget(repository.NostrPublishTargetControlPlane)(controlPlane)
			require.Equal(t, "nostr-publish-outbox:control-plane", controlPlane.Name())
			// Hold the control-plane runner's first retry round until the
			// interop runner has had its chance to adopt the pending row.
			gate := make(chan struct{})
			var rounds atomic.Int32
			controlPlane.publishFn = func(ctx context.Context, ev gonostr.Event, urls []string) ([]PublishResult, error) {
				if rounds.Add(1) == 2 {
					select {
					case <-gate:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				return cpRelays.publish(ctx, ev, urls)
			}

			startRunner(t, interop)
			startRunner(t, controlPlane)

			eventID := tc.publish(t, controlPlane, repo)
			require.ElementsMatch(t, []string{relayA, relayB}, cpRelays.nextCall(t), "inline round contacts every control-plane relay")

			// The event is pending in the control-plane publisher's local outbox.
			entry, found, err := controlPlane.localOutbox.Get(gonostr.MustIDFromHex(eventID))
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, localstore.OutboxPending, entry.State, "relay B has not accepted yet")

			// The interop runner cannot see CP events (separate local outbox files).
			interopRelays.requireNoPendingCalls(t)
			require.False(t, interop.isTracked(eventID), "the interop runner must not adopt a control-plane row")

			close(gate)
			require.Equal(t, []string{relayB}, cpRelays.nextCall(t), "only the relay that has not accepted is retried")
			require.Equal(t, []string{relayB}, cpRelays.nextCall(t))
			require.Equal(t, eventID, receive(t, cpOutbox.published, "row published once relay B accepted"))

			entry, found, err = controlPlane.localOutbox.Get(gonostr.MustIDFromHex(eventID))
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, localstore.OutboxPublished, entry.State)
			interopRelays.requireNoPendingCalls(t)
			cpRelays.requireNoPendingCalls(t)
		})
	}
}

// Each runner discovers only entries from its own local outbox. With the local
// outbox, isolation is architectural (separate bbolt files per publisher), so a
// control-plane entry is never delivered by the interop runner and vice versa.
func TestPublisherRunnerOnlyDiscoversItsOwnTarget(t *testing.T) {
	privateKey := gonostr.Generate()
	repo := repositorytest.NewInMemoryNostrEventRepository()

	cpOutbox := sharedOutbox(repo)
	cpRelays := newScriptedRelays(map[string][]PublishResult{relayA: {{Accepted: true}}})
	controlPlane := newDeliveryTestPublisher(t, cpOutbox, cpRelays, 0, relayA)
	WithPublishTarget(repository.NostrPublishTargetControlPlane)(controlPlane)

	interopOutbox := sharedOutbox(repo)
	interopRelays := newScriptedRelays(map[string][]PublishResult{relayC: {{Accepted: true}}})
	interop := newDeliveryTestPublisher(t, interopOutbox, interopRelays, 0, relayC)

	// Enqueue one event into each publisher's local outbox.
	cpEv := testSignedEvent("left-for-control-plane")
	require.NoError(t, cpEv.Sign(privateKey))
	_, err := controlPlane.localOutbox.Enqueue(localstore.OutboxEntry{Event: *cpEv, Target: repository.NostrPublishTargetControlPlane, EnqueuedAt: controlPlane.now()})
	require.NoError(t, err)

	interopEv := testSignedEvent("left-for-interop")
	require.NoError(t, interopEv.Sign(privateKey))
	_, err = interop.localOutbox.Enqueue(localstore.OutboxEntry{Event: *interopEv, Target: repository.NostrPublishTargetDefault, EnqueuedAt: interop.now()})
	require.NoError(t, err)

	// Start the CP runner - it delivers only the CP event.
	startRunner(t, controlPlane)
	require.Equal(t, []string{relayA}, cpRelays.nextCall(t))
	require.Equal(t, cpEv.ID.Hex(), receive(t, cpOutbox.published, "control-plane row delivered by its runner"))
	cpRelays.requireNoPendingCalls(t)

	// Start the interop runner - it delivers only the interop event.
	startRunner(t, interop)
	require.Equal(t, []string{relayC}, interopRelays.nextCall(t))
	require.Equal(t, interopEv.ID.Hex(), receive(t, interopOutbox.published, "interop row delivered by its runner"))
}

// A dedicated-target runner redelivers even with nostr.publish_enabled off; the
// default runner keeps that gate.
func TestPublisherRedeliveryGate(t *testing.T) {
	require.False(t, (&Publisher{}).redeliveryEnabled())
	require.True(t, (&Publisher{enabled: true}).redeliveryEnabled())
	require.True(t, (&Publisher{target: repository.NostrPublishTargetControlPlane}).redeliveryEnabled())
	require.Equal(t, "nostr-publish-outbox", (&Publisher{}).Name())
}

func TestPublishPresignedEventRefusesInvalidSignatureBeforeRecording(t *testing.T) {
	outbox := newSignalingOutbox()
	relays := newScriptedRelays(nil)
	publisher := newDeliveryTestPublisher(t, outbox, relays, 0, relayA)

	ev := testSignedEvent("forged")
	require.NoError(t, ev.Sign(gonostr.Generate()))
	ev.Content = "tampered"
	_, err := publisher.PublishPresignedEvent(context.Background(), *ev, "config-fabric.desired")
	require.Error(t, err)
	depth, err := outbox.CountUnpublished(context.Background())
	require.NoError(t, err)
	require.Zero(t, depth)
	relays.requireNoPendingCalls(t)
}
