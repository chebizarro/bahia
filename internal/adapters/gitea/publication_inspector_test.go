package gitea

import (
	"context"
	"testing"

	"fiatjaf.com/nostr"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
)

type inspectionSubscriber struct {
	sub     *nostrAdapter.MergedSubscription
	filters []nostr.Filter
}

func (s *inspectionSubscriber) SubscribeAllWithEOSE(_ context.Context, filters []nostr.Filter) (*nostrAdapter.MergedSubscription, error) {
	s.filters = filters
	return s.sub, nil
}

func TestPublicationInspectorDrainsBufferedEventsAtEOSE(t *testing.T) {
	for _, scenario := range []string{"found", "absent", "invalid", "duplicate", "ambiguous"} {
		t.Run(scenario, func(t *testing.T) {
			event := nostr.Event{Kind: nostr.Kind(kinds.HiveCIWorkflowRun), CreatedAt: nostr.Now()}
			require.NoError(t, controlplane.SignGoNostrEvent(context.Background(), newTestSigner(t), &event))
			filter := nostr.Filter{Authors: []nostr.PubKey{event.PubKey}, Kinds: []nostr.Kind{event.Kind}}
			events, eose := make(chan *nostr.Event, 2), make(chan struct{})
			if scenario != "absent" {
				if scenario == "invalid" {
					event.Content = "tampered"
				}
				events <- &event
			}
			if scenario == "duplicate" {
				events <- &event
			}
			if scenario == "ambiguous" {
				second := event
				second.Content = "different signed event"
				require.NoError(t, controlplane.SignGoNostrEvent(context.Background(), newTestSigner(t), &second))
				events <- &second
			}
			close(eose)
			subscriber := &inspectionSubscriber{sub: &nostrAdapter.MergedSubscription{Events: events, EndOfStoredEvents: eose}}
			found, err := NewRelayPublicationInspector(subscriber).FindPublishedEvent(context.Background(), filter)
			require.Equal(t, []nostr.Filter{filter}, subscriber.filters)
			switch scenario {
			case "invalid", "ambiguous":
				require.Error(t, err)
				require.Nil(t, found)
			case "absent":
				require.NoError(t, err)
				require.Nil(t, found)
			default:
				require.NoError(t, err)
				require.Equal(t, &event, found)
			}
		})
	}
}

func TestPublicationInspectorRejectsClosedOrCanceledHistory(t *testing.T) {
	for _, scenario := range []string{"CLOSED", "stream-ended", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			events := make(chan *nostr.Event)
			closed := make(chan nostrAdapter.RelayClosed, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch scenario {
			case "CLOSED":
				closed <- nostrAdapter.RelayClosed{Reason: "auth-required"}
			case "stream-ended":
				close(events)
			case "canceled":
				cancel()
			}
			subscriber := &inspectionSubscriber{sub: &nostrAdapter.MergedSubscription{Events: events, Closed: closed, EndOfStoredEvents: make(chan struct{})}}
			found, err := NewRelayPublicationInspector(subscriber).FindPublishedEvent(ctx, nostr.Filter{})
			require.Error(t, err)
			require.Nil(t, found)
		})
	}
}

func TestInitiationCrashBeforeSendRemainsUnconfirmed(t *testing.T) {
	// A durable send intent does not prove the send happened. Neither missing
	// history nor a later retry may manufacture a successful publication.
	store := NewMemoryInitiationStore()
	req := arcanaStartRequest(testMirrorReadCredentialRef)
	record, _, err := store.Claim(context.Background(), req)
	require.NoError(t, err)
	event := &nostr.Event{Kind: nostr.Kind(kinds.HiveCIWorkflowRun), CreatedAt: nostr.Now()}
	require.NoError(t, controlplane.SignGoNostrEvent(context.Background(), newTestSigner(t), event))
	record.RunEvent = event
	record.Stage = StageRequestReady
	require.NoError(t, store.Advance(context.Background(), StageClaimed, record))
	relay := newInitiationRelay(t)
	initiator := &Initiator{store: &crashInitiationStore{store, StageRequestUnconfirmed, false}, publisher: relay, inspection: relay}
	err = initiator.publishStage(context.Background(), record, event, StageRequestUnconfirmed, StageRequestPublished)
	require.ErrorIs(t, err, errSimulatedCrash)
	record, err = store.Get(context.Background(), req.SourceEventID)
	require.NoError(t, err)
	initiator.store = store
	result, err := initiator.resumeInitiation(context.Background(), record)
	require.ErrorIs(t, err, ErrPublishUnconfirmed)
	require.Nil(t, result)
	require.Empty(t, relay.sends)
}
