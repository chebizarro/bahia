package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/coder/websocket"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestBackupRunHistoryRequiresExactAllRelayEOSEAndStableConnection(t *testing.T) {
	first := newNIP11LimitRelay(t, map[string]int{"max_limit": 20})
	second := newNIP11LimitRelay(t, map[string]int{"max_limit": 20})
	pool := NewRelayPool([]string{first.url, second.url}, zap.NewNop())
	defer pool.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "events.db"))
	require.NoError(t, err)
	defer store.Close()
	service := gonostr.Generate().Public()
	coordinate := "backup-run:run-1"
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	proof, err := pool.InspectBackupRunHistory(ctx, store, service, coordinate)
	require.NoError(t, err)
	require.NotNil(t, proof)
	defer proof.Close()
	require.Len(t, proof.RelayURLs, 2)
	require.NoError(t, proof.StillCurrent())
	for _, relay := range []*nip11LimitRelay{first, second} {
		reqs := relay.requests()
		require.NotEmpty(t, reqs)
		filter := reqs[0][0]
		require.Equal(t, []gonostr.Kind{gonostr.Kind(kinds.CASControlState)}, filter.Kinds)
		require.Equal(t, []gonostr.PubKey{service}, filter.Authors)
		require.Equal(t, gonostr.TagMap{"d": {coordinate}}, filter.Tags)
		require.LessOrEqual(t, filter.Limit, 20)
	}
	pool.recycleBackupHistoryRelay(proof.RelayURLs[0], proof.ConnectionEpoch[proof.RelayURLs[0]])
	require.ErrorContains(t, proof.StillCurrent(), "epoch changed")
}

func TestBackupRunHistoryStalledEOSECannotProveAbsenceAndRetriesOnFreshConnection(t *testing.T) {
	reqSeen := make(chan struct{}, 1)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") == "application/nostr+json" {
			w.Header().Set("Content-Type", "application/nostr+json")
			_, _ = w.Write([]byte(`{"name":"history","limitation":{"max_limit":20}}`))
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			_, msg, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var frame []json.RawMessage
			if json.Unmarshal(msg, &frame) != nil || len(frame) < 2 {
				continue
			}
			var verb, subID string
			_ = json.Unmarshal(frame[0], &verb)
			_ = json.Unmarshal(frame[1], &subID)
			if verb != "REQ" {
				continue
			}
			if requests.Add(1) == 1 {
				select {
				case reqSeen <- struct{}{}:
				default:
				}
				continue // first connection never reaches EOSE
			}
			_ = conn.Write(r.Context(), websocket.MessageText, []byte(`["EOSE",`+string(frame[1])+`]`))
		}
	}))
	defer server.Close()
	pool := NewRelayPool([]string{"ws" + strings.TrimPrefix(server.URL, "http")}, zap.NewNop())
	defer pool.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "events.db"))
	require.NoError(t, err)
	defer store.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	result := make(chan error, 1)
	go func() {
		proof, err := pool.InspectBackupRunHistory(ctx, store, gonostr.Generate().Public(), "backup-run:run-1")
		if proof != nil {
			proof.Close()
		}
		result <- err
	}()
	select {
	case <-reqSeen:
		cancel()
	case <-ctx.Done():
		t.Fatal("stalled relay was not queried")
	}
	require.Error(t, <-result)
	ctx2, cancel2 := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel2()
	proof, err := pool.InspectBackupRunHistory(ctx2, store, gonostr.Generate().Public(), "backup-run:run-1")
	require.NoError(t, err)
	proof.Close()
	require.GreaterOrEqual(t, requests.Load(), int32(2))
}

func TestBackupRunHistoryRunnerPersistsRetryThenRefusesExpiredRequestAfterRestart(t *testing.T) {
	relay := newNIP11LimitRelay(t, map[string]int{"max_limit": 20})
	pool := NewRelayPool([]string{relay.url}, zap.NewNop())
	defer pool.Close()
	dir := t.TempDir()
	store, err := localstore.Open(filepath.Join(dir, "events.db"))
	require.NoError(t, err)
	defer store.Close()
	outboxPath := filepath.Join(dir, "outbox.db")
	outbox, err := localstore.OpenOutbox(outboxPath)
	require.NoError(t, err)
	actor, service := gonostr.Generate(), gonostr.Generate()
	now := time.Now().UTC().Truncate(time.Second)
	coordinate := "backup-run:run-1"
	request := gonostr.Event{Kind: gonostr.Kind(kinds.CASControlState), CreatedAt: gonostr.Timestamp(now.Unix()),
		Tags: gonostr.Tags{{"d", coordinate}, {"intent_id", "intent-1"}, {"expiration", fmt.Sprint(now.Add(time.Minute).Unix())}}, Content: `{}`}
	require.NoError(t, request.Sign(actor))
	_, inserted, err := outbox.PutBackupRunPending(localstore.BackupRunPending{
		IntentID: "intent-1", Coordinate: coordinate, RequestEvent: request, Actor: actor.Public().Hex(),
		ServicePubkey: service.Public().Hex(), ReceivedAt: now, ExpiresAt: now.Add(time.Minute),
	})
	require.NoError(t, err)
	require.True(t, inserted)
	runner, err := NewBackupRunHistoryRunner(outbox, pool, store, zap.NewNop())
	require.NoError(t, err)
	runner.now = func() time.Time { return now }
	_, err = runner.ReconcileOnce(t.Context())
	require.ErrorContains(t, err, "cross-process service-key signer fence")
	pending, err := outbox.GetBackupRunPending("intent-1", coordinate, request.ID.Hex())
	require.NoError(t, err)
	require.Equal(t, localstore.BackupRunPendingState, pending.State)
	require.Equal(t, 1, pending.Attempts)
	require.Equal(t, request.ID, pending.RequestEvent.ID)
	admission, err := outbox.GetBackupRunAdmission("intent-1", coordinate, request.ID.Hex())
	require.NoError(t, err)
	require.Nil(t, admission)
	require.NoError(t, outbox.Close())
	outbox, err = localstore.OpenOutbox(outboxPath)
	require.NoError(t, err)
	defer outbox.Close()
	runner, err = NewBackupRunHistoryRunner(outbox, pool, store, zap.NewNop())
	require.NoError(t, err)
	runner.now = func() time.Time { return now.Add(time.Minute) }
	_, err = runner.ReconcileOnce(t.Context())
	require.NoError(t, err)
	refused, err := outbox.GetBackupRunPending("intent-1", coordinate, request.ID.Hex())
	require.NoError(t, err)
	require.Equal(t, localstore.BackupRunRefusedState, refused.State)
	require.Equal(t, request.ID, refused.RequestEvent.ID)
	admission, err = outbox.GetBackupRunAdmission("intent-1", coordinate, request.ID.Hex())
	require.NoError(t, err)
	require.Nil(t, admission, "expiry cannot create a signed run or accepted status")
}

func TestBackupRunHistoryLocalPreledgerCoordinateVetoesEmptyRelay(t *testing.T) {
	relay := newNIP11LimitRelay(t, map[string]int{"max_limit": 20})
	pool := NewRelayPool([]string{relay.url}, zap.NewNop())
	defer pool.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "events.db"))
	require.NoError(t, err)
	defer store.Close()
	service := gonostr.Generate()
	coordinate := "backup-run:old-coordinate"
	// No modern topic tag or envelope is required for the veto.
	old := gonostr.Event{Kind: gonostr.Kind(kinds.CASControlState), CreatedAt: gonostr.Now(), Tags: gonostr.Tags{{"d", coordinate}}, Content: "legacy"}
	require.NoError(t, old.Sign(service))
	_, err = store.SaveEvent(old)
	require.NoError(t, err)
	proof, err := pool.InspectBackupRunHistory(t.Context(), store, service.Public(), coordinate)
	require.Nil(t, proof)
	require.ErrorContains(t, err, "already exists in local relay cache")
	require.Empty(t, relay.requests(), "a known coordinate need not issue a relay REQ")
}

func TestBackupRunHistoryProofRejectsTerminalCLOSEDAfterEOSE(t *testing.T) {
	closeREQ := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") == "application/nostr+json" {
			w.Header().Set("Content-Type", "application/nostr+json")
			_, _ = w.Write([]byte(`{"name":"history","limitation":{"max_limit":20}}`))
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			_, msg, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var frame []json.RawMessage
			if json.Unmarshal(msg, &frame) != nil || len(frame) < 2 {
				continue
			}
			var verb string
			_ = json.Unmarshal(frame[0], &verb)
			if verb != "REQ" {
				continue
			}
			if err := conn.Write(r.Context(), websocket.MessageText, []byte(`["EOSE",`+string(frame[1])+`]`)); err != nil {
				return
			}
			select {
			case <-closeREQ:
			case <-r.Context().Done():
				return
			}
			_ = conn.Write(r.Context(), websocket.MessageText, []byte(`["CLOSED",`+string(frame[1])+`,"blocked: policy refusal"]`))
			return
		}
	}))
	defer server.Close()
	pool := NewRelayPool([]string{"ws" + strings.TrimPrefix(server.URL, "http")}, zap.NewNop())
	defer pool.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "events.db"))
	require.NoError(t, err)
	defer store.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	proof, err := pool.InspectBackupRunHistory(ctx, store, gonostr.Generate().Public(), "backup-run:closed-after-eose")
	require.NoError(t, err)
	defer proof.Close()
	require.NoError(t, proof.StillCurrent())
	close(closeREQ)
	select {
	case closed := <-proof.sub.Closed:
		require.True(t, closed.Terminal)
	case <-ctx.Done():
		t.Fatal("terminal CLOSED was not observed")
	}
	require.ErrorIs(t, proof.sub.GaveUp(), ErrSubscriptionGaveUp)
	require.ErrorContains(t, proof.StillCurrent(), "gave up")
}

func TestBackupRunHistoryProofAndCancellationAfterEOSENeverPass(t *testing.T) {
	relay := newNIP11LimitRelay(t, map[string]int{"max_limit": 20})
	pool := NewRelayPool([]string{relay.url}, zap.NewNop())
	defer pool.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "events.db"))
	require.NoError(t, err)
	defer store.Close()
	ctx, cancel := context.WithCancel(t.Context())
	proof, err := pool.InspectBackupRunHistory(ctx, store, gonostr.Generate().Public(), "backup-run:cancel-after-eose")
	require.NoError(t, err)
	defer proof.Close()
	require.NoError(t, proof.StillCurrent())
	cancel()
	require.ErrorContains(t, proof.StillCurrent(), "context expired")
	require.ErrorIs(t, backupRunHistoryCanceled(proof.sub, context.Canceled), context.Canceled)
}
