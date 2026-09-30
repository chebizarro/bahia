package nostr

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
)

// sharedOutbox wraps one repository in a signalingOutbox per runner so each
// runner's discovery passes are observed separately while both read the same
// rows.
func sharedOutbox(repo *repository.InMemoryNostrEventRepository) *signalingOutbox {
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
				rec := nostrEventRecordFromEvent(*ev, "config-fabric.desired")
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
			ctx := context.Background()
			repo := repository.NewInMemoryNostrEventRepository()
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

			startRunner(t, interop, interopOutbox)
			startRunner(t, controlPlane, cpOutbox)

			eventID := tc.publish(t, controlPlane, repo)
			require.ElementsMatch(t, []string{relayA, relayB}, cpRelays.nextCall(t), "inline round contacts every control-plane relay")

			rec, err := repo.GetByID(ctx, eventID)
			require.NoError(t, err)
			require.Equal(t, repository.NostrPublishStatePending, rec.PublishState, "relay B has not accepted yet")
			require.Equal(t, repository.NostrPublishTargetControlPlane, rec.PublishTarget)

			discoveryPass(t, interop, interopOutbox)
			interopRelays.requireNoPendingCalls(t)
			require.False(t, interop.isTracked(eventID), "the interop runner must not adopt a control-plane row")

			close(gate)
			require.Equal(t, []string{relayB}, cpRelays.nextCall(t), "only the relay that has not accepted is retried")
			require.Equal(t, []string{relayB}, cpRelays.nextCall(t))
			require.Equal(t, eventID, receive(t, cpOutbox.published, "row published once relay B accepted"))

			rec, err = repo.GetByID(ctx, eventID)
			require.NoError(t, err)
			require.Equal(t, repository.NostrPublishStatePublished, rec.PublishState)
			discoveryPass(t, interop, interopOutbox)
			interopRelays.requireNoPendingCalls(t)
			cpRelays.requireNoPendingCalls(t)
		})
	}
}

// Each runner drains only its own target: a row left pending for the interop
// pool by a previous process is not delivered to control-plane relays, and
// vice versa.
func TestPublisherRunnerOnlyDiscoversItsOwnTarget(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewInMemoryNostrEventRepository()
	privateKey := gonostr.Generate()

	record := func(content, target string) string {
		ev := testSignedEvent(content)
		require.NoError(t, ev.Sign(privateKey))
		rec := nostrEventRecordFromEvent(*ev, "delivery.test")
		rec.PublishState = repository.NostrPublishStatePending
		rec.PublishTarget = target
		_, err := repo.Record(ctx, rec)
		require.NoError(t, err)
		return ev.ID.Hex()
	}
	interopID := record("left-for-interop", repository.NostrPublishTargetDefault)
	cpID := record("left-for-control-plane", repository.NostrPublishTargetControlPlane)

	cpOutbox := sharedOutbox(repo)
	cpRelays := newScriptedRelays(map[string][]PublishResult{relayA: {{Accepted: true}}})
	controlPlane := newDeliveryTestPublisher(t, cpOutbox, cpRelays, 0, relayA)
	WithPublishTarget(repository.NostrPublishTargetControlPlane)(controlPlane)
	startRunner(t, controlPlane, cpOutbox)

	require.Equal(t, []string{relayA}, cpRelays.nextCall(t))
	require.Equal(t, cpID, receive(t, cpOutbox.published, "control-plane row delivered by its runner"))
	discoveryPass(t, controlPlane, cpOutbox)
	cpRelays.requireNoPendingCalls(t)

	rec, err := repo.GetByID(ctx, interopID)
	require.NoError(t, err)
	require.Equal(t, repository.NostrPublishStatePending, rec.PublishState, "the interop row waits for the interop runner")

	interopOutbox := sharedOutbox(repo)
	interopRelays := newScriptedRelays(map[string][]PublishResult{relayC: {{Accepted: true}}})
	interop := newDeliveryTestPublisher(t, interopOutbox, interopRelays, 0, relayC)
	startRunner(t, interop, interopOutbox)
	require.Equal(t, []string{relayC}, interopRelays.nextCall(t))
	require.Equal(t, interopID, receive(t, interopOutbox.published, "interop row delivered by its runner"))
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
