package nostr

import (
	"context"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestPaymentCanonicalDBLessLocalStoreSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	script := newRelayScript()
	runID := uuid.New()

	first := startLocalHistoryDaemon(t, dir, script)
	firstView := NewPaymentCanonicalPublisher(first.projector, &mockConfidentialEncryptor{}, zap.NewNop())
	firstService := service.NewPaymentService(nil, zap.NewNop())
	firstService.SetCPStatePublisher(firstView)
	firstService.SetCanonicalView(firstView)
	record, err := firstService.RecordPayment(ctx, runID, "worker", "https://mint", 41, "proof")
	require.NoError(t, err)
	firstPublishCalls := script.totalCalls()

	replayed, err := firstService.RecordPayment(ctx, runID, "worker", "https://mint", 41, "proof")
	require.NoError(t, err)
	require.Equal(t, record.ID, replayed.ID)
	require.Equal(t, firstPublishCalls, script.totalCalls(), "same token must not sign another record")
	first.close()

	restarted := startLocalHistoryDaemon(t, dir, script)
	restartedView := NewPaymentCanonicalPublisher(restarted.projector, &mockConfidentialEncryptor{}, zap.NewNop())
	restartedService := service.NewPaymentService(nil, zap.NewNop())
	restartedService.SetCPStatePublisher(restartedView)
	restartedService.SetCanonicalView(restartedView)
	history, err := restartedService.GetPaymentHistory(ctx, "worker", 10)
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, record.ID, history[0].ID)
	require.NoError(t, restartedService.MarkPaymentSent(ctx, record.ID))

	history, err = restartedService.GetPaymentHistory(ctx, "worker", 10)
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, domain.PaymentStatusSent, history[0].Status)

	servicePubkey, err := publicKeyHexFromPrivateKeyHex(projectorTestConfig().PrivateKey)
	require.NoError(t, err)
	pubkey := nostr.MustPubKeyFromHex(servicePubkey)
	events := 0
	for range restarted.store.QueryEvents(nostr.Filter{
		Kinds:   []nostr.Kind{KindCASControlState},
		Authors: []nostr.PubKey{pubkey},
		Tags:    nostr.TagMap{"t": []string{kinds.CPStateTopicPaymentRecord}},
	}) {
		events++
	}
	require.Equal(t, 1, events, "state transition must replace the same local coordinate")
}
