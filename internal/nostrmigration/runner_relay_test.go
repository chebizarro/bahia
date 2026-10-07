package nostrmigration

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr/eventstore/slicestore"
	"fiatjaf.com/nostr/khatru"
	"fiatjaf.com/nostr/nip11"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository/repositorytest"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestRunnerRelayBackfillPastRelayMaxLimit: the relay backfill stops paging
// at the first page shorter than BackfillLimit. Against a relay whose NIP-11
// max_limit is lower, a page capped at max_limit used to look like the last
// one and the rest of the legacy history was silently left unmigrated
// (bahia-irsry.49). The shared pool now pages capped answers itself, so every
// stored legacy event is migrated.
func TestRunnerRelayBackfillPastRelayMaxLimit(t *testing.T) {
	const maxLimit, stored = 5, 12
	relay := khatru.NewRelay()
	store := &slicestore.SliceStore{}
	require.NoError(t, store.Init())
	t.Cleanup(store.Close)
	relay.UseEventstore(store, maxLimit)
	relay.Info.Limitation = &nip11.RelayLimitationDocument{MaxLimit: maxLimit}
	for i := range stored {
		ev := signedLegacyEvent(t, kinds.PackagePromotionRequest, time.Unix(int64(1000+i), 0).UTC())
		_, err := relay.AddEvent(t.Context(), *ev)
		require.NoError(t, err)
	}
	server := httptest.NewServer(relay)
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	pool := nostrAdapter.NewRelayPool([]string{"ws" + strings.TrimPrefix(server.URL, "http")}, zap.NewNop())
	defer pool.Close()
	pool.Connect(ctx)
	publisher := &captureMigrationPublisher{outcomes: []PublishOutcome{{Accepted: true}}}
	runner := NewRunner(repositorytest.NewInMemoryNostrEventRepository(), publisher, pool,
		Config{PrivateKey: deterministicPrivateKey(t), RelayBackfill: true, BackfillLimit: 50}, zap.NewNop())

	require.NoError(t, runner.Run(ctx))
	require.Len(t, publisher.events, stored, "every stored legacy event, not the newest max_limit")
}
