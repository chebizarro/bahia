package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"fiatjaf.com/nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

type continuityTestPool struct {
	subscription *nostradapter.MergedSubscription
	filters      chan []nostr.Filter
	closed       int
	auth         int
}

func (p *continuityTestPool) SubscribeAllWithEOSE(_ context.Context, filters []nostr.Filter) (*nostradapter.MergedSubscription, error) {
	p.filters <- filters
	return p.subscription, nil
}
func (p *continuityTestPool) AuthenticateRelay(context.Context, string) error { p.auth++; return nil }
func (p *continuityTestPool) RecordRelayClosed(string, string)                { p.closed++ }
func (*continuityTestPool) RecordRelayReREQ()                                 {}

func TestContinuityDefinitionsEOSEBackfillAndRealtime(t *testing.T) {
	_, h, store, _, _ := continuityFixture(t, NewFleetOperatorGate([]string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)}))
	store.applied = make(chan struct{}, 16)
	stream := make(chan *nostr.Event, 16)
	eose := make(chan struct{})
	pool := &continuityTestPool{subscription: &nostradapter.MergedSubscription{Events: stream, EndOfStoredEvents: eose}, filters: make(chan []nostr.Filter, 1)}
	h.pool = pool
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	defer func() { cancel(); require.NoError(t, <-done) }()
	filters := <-pool.filters
	require.Len(t, filters, 1)
	require.Equal(t, []nostr.PubKey{testNostrPubKeyFromPrivateKey(t, testRequesterKey)}, filters[0].Authors)
	require.ElementsMatch(t, []nostr.Kind{nostradapter.KindContinuityProfile, nostradapter.KindFailoverPolicy, nostradapter.KindReplicationPolicy, nostradapter.KindRecoveryWorkflow}, filters[0].Kinds)
	require.Equal(t, nostr.Timestamp(1), filters[0].Since)
	require.Zero(t, filters[0].Limit, "a cold rebuild must not truncate definitions")
	definitions := continuityDefinitionEvents(t, testRequesterKey)
	for i := range definitions {
		stream <- &definitions[i]
	}
	for range definitions {
		<-store.applied
	}
	select {
	case <-h.ready:
		t.Fatal("EVENT delivery cannot complete backfill before EOSE")
	default:
	}
	require.Equal(t, int32(4), store.mutations.Load())
	close(eose)
	<-h.ready
	for i := range definitions {
		stream <- &definitions[i]
	}
	for range definitions {
		<-store.applied
	}
	require.Equal(t, int32(4), store.mutations.Load(), "replay must not repeat projection mutations")
	newer := definitions[1]
	newer.CreatedAt++
	require.NoError(t, newer.Sign(testNostrSecretKey(t, testRequesterKey)))
	stream <- &newer
	<-store.applied
	recipe, ok := store.GetRecipe("api", domain.ContinuityRecipeKindFailover)
	require.True(t, ok)
	require.Equal(t, newer.ID.Hex(), recipe.SourceEventID, "EOSE must leave the REQ open for replacements")
	require.Equal(t, int32(5), store.mutations.Load())
	stream <- &definitions[1]
	<-store.applied
	require.Equal(t, int32(5), store.mutations.Load(), "out of order history must not overwrite realtime state")
}

func TestContinuityDefinitionsFailClosedBeforeStorage(t *testing.T) {
	for _, auth := range []string{"outsider", "empty", "nil"} {
		t.Run(auth, func(t *testing.T) {
			gate := NewFleetOperatorGate([]string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)})
			key := testOtherKey
			if auth == "empty" {
				gate = NewFleetOperatorGate(nil)
			}
			if auth == "nil" {
				gate = nil
			}
			_, h, store, _, _ := continuityFixture(t, gate)
			for _, event := range continuityDefinitionEvents(t, key) {
				require.Error(t, h.handleDefinition(t.Context(), &event))
			}
			require.Zero(t, store.touches.Load())
			if auth != "outsider" {
				_, err := h.definitionFilters()
				require.Error(t, err, "empty authors must never become a broad REQ")
			}
		})
	}
}

func TestContinuityDefinitionsRejectInvalidSignatureAndStandby(t *testing.T) {
	_, h, store, _, _ := continuityFixture(t, NewFleetOperatorGate([]string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)}))
	event := continuityDefinitionEvents(t, testRequesterKey)[1]
	event.Content += " "
	require.Error(t, h.handleDefinition(t.Context(), &event))
	event.Kind = nostradapter.KindStandbyNodeDefinition
	require.NoError(t, event.Sign(testNostrSecretKey(t, testRequesterKey)))
	require.ErrorContains(t, h.handleDefinition(t.Context(), &event), "unsupported continuity definition")
	require.Zero(t, store.touches.Load())
}

func TestContinuityDefinitionsClosedAndCancellationAreNotEOSE(t *testing.T) {
	for _, reason := range []string{"blocked: scope rejected", "auth-required: identify"} {
		_, h, _, _, _ := continuityFixture(t, nil)
		closed := make(chan nostradapter.RelayClosed, 1)
		closed <- nostradapter.RelayClosed{RelayURL: "wss://relay.example", Reason: reason}
		pool := &continuityTestPool{}
		h.pool = pool
		err := h.consumeDefinitions(t.Context(), &nostradapter.MergedSubscription{Closed: closed}, nostradapter.DefaultBackoff())
		require.ErrorContains(t, err, "CLOSED")
		require.Equal(t, 1, pool.closed)
		if nostradapter.IsAuthRequiredReason(reason) {
			require.Equal(t, 1, pool.auth)
		}
		select {
		case <-h.ready:
			t.Fatal("CLOSED is not successful backfill")
		default:
		}
	}
	_, h, _, _, _ := continuityFixture(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, h.consumeDefinitions(ctx, &nostradapter.MergedSubscription{}, nostradapter.DefaultBackoff()), context.Canceled)
	select {
	case <-h.ready:
		t.Fatal("cancellation is not successful backfill")
	default:
	}
}

func TestContinuityCommandWaitsForHistoryBeforeStorage(t *testing.T) {
	gate := NewFleetOperatorGate([]string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)})
	_, h, store, _, _ := continuityFixture(t, gate)
	event := continuityRequest(t, ContextVMMethodContinuityFailover, testRequesterKey, 0)
	var rpc ContextVMJSONRPCRequest
	require.NoError(t, json.Unmarshal([]byte(event.Content), &rpc))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := gate.wrap(h.handleFailoverRequest)(ctx, ContextVMRequest{Event: event, RPC: rpc, ProgressToken: "continuity-test"})
	require.True(t, errors.Is(err, context.Canceled))
	require.Zero(t, store.touches.Load())
}

func TestContinuityDefinitionsEqualTimestampUsesLowestID(t *testing.T) {
	original := continuityDefinitionEvents(t, testRequesterKey)[1]
	replacement := original
	replacement.Tags = append(append(nostr.Tags(nil), original.Tags...), nostr.Tag{"test", "replacement"})
	require.NoError(t, replacement.Sign(testNostrSecretKey(t, testRequesterKey)))
	expected := original.ID.Hex()
	if replacement.ID.Hex() < expected {
		expected = replacement.ID.Hex()
	}
	for _, order := range [][]nostr.Event{{original, replacement}, {replacement, original}} {
		_, h, store, _, _ := continuityFixture(t, NewFleetOperatorGate([]string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)}))
		for _, event := range order {
			require.NoError(t, h.handleDefinition(t.Context(), &event))
		}
		recipe, ok := store.GetRecipe("api", domain.ContinuityRecipeKindFailover)
		require.True(t, ok)
		require.Equal(t, expected, recipe.SourceEventID)
	}
}

func TestContinuityMissingPoolFailsWithoutPanic(t *testing.T) {
	_, h, _, _, _ := continuityFixture(t, NewFleetOperatorGate([]string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)}))
	require.ErrorContains(t, h.Run(t.Context()), "not configured")
}
