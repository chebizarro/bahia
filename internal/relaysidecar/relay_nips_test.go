package relaysidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/slicestore"
	"fiatjaf.com/nostr/eventstore/wrappers"
	"fiatjaf.com/nostr/nip11"
	"fiatjaf.com/nostr/nip77"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// publishFrame sends EVENT and returns the relay's OK for it.
func (c *rawRelayClient) publishFrame(event nostr.Event) relayFrame {
	c.t.Helper()
	c.send("EVENT", event)
	for {
		frame := c.next()
		if frame.label == "OK" && frame.subID == event.ID.Hex() {
			return frame
		}
	}
}

func (c *rawRelayClient) storedIDs(subID string, filter nostr.Filter) []nostr.ID {
	c.t.Helper()
	var ids []nostr.ID
	for id := range c.subscribe(subID, filter) {
		ids = append(ids, id)
	}
	c.send("CLOSE", subID)
	return ids
}

func signedNowEvent(t *testing.T, sk nostr.SecretKey, kind nostr.Kind, offset int, tags nostr.Tags, content string) nostr.Event {
	t.Helper()
	return signedStoreEvent(t, sk, kind, nostr.Now()-600+nostr.Timestamp(offset), tags, content)
}

// TestSidecarNIP09DeletionOverWebsocket: a kind-5 request is acknowledged,
// removes the author's `e`/`a` targets from REQ results, and the relay then
// refuses to re-accept them (OK false), while newer versions of a deleted
// coordinate are accepted.
func TestSidecarNIP09DeletionOverWebsocket(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), fanoutTestTimeout)
	defer cancel()
	_, relayURL := startSidecarForFanoutTest(t)
	client := dialRawRelay(t, ctx, relayURL)
	alice := nostr.Generate()

	note := signedNowEvent(t, alice, 1, 0, nil, "note")
	client.publish(note)
	require.Equal(t, []nostr.ID{note.ID}, client.storedIDs("note", nostr.Filter{IDs: []nostr.ID{note.ID}}))

	v1 := signedNowEvent(t, alice, 30078, 1, nostr.Tags{{"d", "app"}}, "v1")
	client.publish(v1)
	address := fmt.Sprintf("30078:%s:app", alice.Public().Hex())
	deletion := signedNowEvent(t, alice, nostr.KindDeletion, 10, nostr.Tags{{"e", note.ID.Hex()}, {"a", address}}, "")
	client.publish(deletion)

	require.Empty(t, client.storedIDs("after-delete", nostr.Filter{IDs: []nostr.ID{note.ID, v1.ID}}))
	require.Equal(t, []nostr.ID{deletion.ID}, client.storedIDs("tombstone", nostr.Filter{Kinds: []nostr.Kind{nostr.KindDeletion}, Authors: []nostr.PubKey{alice.Public()}}))
	for _, event := range []nostr.Event{note, v1} {
		ok := client.publishFrame(event)
		require.False(t, ok.ok, "kind %d re-accepted after deletion", event.Kind)
		require.True(t, strings.HasPrefix(ok.reason, "blocked: "), ok.reason)
		require.Contains(t, ok.reason, "NIP-09")
	}
	v2 := signedNowEvent(t, alice, 30078, 11, nostr.Tags{{"d", "app"}}, "v2")
	client.publish(v2)
	require.Equal(t, []nostr.ID{v2.ID}, client.storedIDs("newer", nostr.Filter{Kinds: []nostr.Kind{30078}, Authors: []nostr.PubKey{alice.Public()}, Tags: nostr.TagMap{"d": {"app"}}}))
}

// TestSidecarNIP40AndEphemeralRetention: an already-expired event is refused;
// ephemeral kinds, including ContextVM 25910 and gift wrap 21059, are relayed
// but never stored.
func TestSidecarNIP40AndEphemeralRetention(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), fanoutTestTimeout)
	defer cancel()
	server, relayURL := startSidecarForFanoutTest(t)
	client := dialRawRelay(t, ctx, relayURL)
	sk := nostr.Generate()

	expiredEvent := signedNowEvent(t, sk, 1, 0, nostr.Tags{{"expiration", strconv.FormatInt(int64(nostr.Now()-1), 10)}}, "")
	ok := client.publishFrame(expiredEvent)
	require.False(t, ok.ok)
	require.Contains(t, ok.reason, "invalid: event has expired")

	ephemeral := []nostr.Kind{kinds.ContextVMMessage, kinds.ContextVMEphemeralGiftWrap}
	live := client.subscribe("live", nostr.Filter{Kinds: ephemeral})
	require.Empty(t, live)
	for i, kind := range ephemeral {
		event := signedNowEvent(t, sk, kind, i, nostr.Tags{{"p", sk.Public().Hex()}}, "transport")
		client.send("EVENT", event)
		// Live delivery and OK both arrive; the store stays empty.
		var delivered, acked bool
		for !delivered || !acked {
			frame := client.next()
			switch {
			case frame.label == "EVENT" && frame.subID == "live" && frame.event.ID == event.ID:
				delivered = true
			case frame.label == "OK" && frame.subID == event.ID.Hex():
				require.True(t, frame.ok, frame.reason)
				acked = true
			}
		}
	}
	require.Empty(t, client.storedIDs("replay", nostr.Filter{Kinds: ephemeral}))
	count, err := server.store.Count(ctx, nostr.Filter{Kinds: ephemeral})
	require.NoError(t, err)
	require.Zero(t, count)
}

func startHTTPTestServer(t *testing.T, server *Server) string {
	t.Helper()
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	return httpServer.URL
}

func fetchNIP11(t *testing.T, relayURL string) nip11.RelayInformationDocument {
	t.Helper()
	var info nip11.RelayInformationDocument
	body := fetchNIP11Body(t, relayURL)
	require.NoError(t, json.Unmarshal(body, &info), string(body))
	return info
}

func fetchNIP11Body(t *testing.T, relayURL string) []byte {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, relayURL, nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "application/nostr+json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer closeSidecarTest(t, resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return body
}

// TestSidecarNIP11AdvertisesAccurateCapabilities covers the NIP-11 advertisement contract: NIPs 9, 40, 45
// and 77 are advertised, limits match what the relay enforces, retention
// describes the kind classes, and restricted_writes follows the allow list.
// created_at_lower_limit is absent: the one-year cap only covers regular and
// ephemeral kinds, which the field cannot express.
func TestSidecarNIP11AdvertisesAccurateCapabilities(t *testing.T) {
	server, relayURL := startSidecarForFanoutTest(t)
	info := fetchNIP11(t, relayURL)
	var nips []int
	for _, nip := range info.SupportedNIPs {
		switch v := nip.(type) {
		case float64:
			nips = append(nips, int(v))
		case int:
			nips = append(nips, v)
		}
	}
	for _, want := range []int{1, 9, 11, 40, 42, 45, 77} {
		require.Contains(t, nips, want)
	}
	require.NotNil(t, info.Limitation)
	require.Equal(t, server.cfg.MaxQueryLimit, info.Limitation.MaxLimit)
	require.Equal(t, server.cfg.MaxQueryLimit, info.Limitation.DefaultLimit)
	require.Equal(t, 65535, info.Limitation.MaxContentLength)
	require.Equal(t, int(server.relay.MaxMessageSize), info.Limitation.MaxMessageLength)
	require.Zero(t, info.Limitation.CreatedAtLowerLimit)
	var raw struct {
		Limitation map[string]json.RawMessage `json:"limitation"`
	}
	require.NoError(t, json.Unmarshal(fetchNIP11Body(t, relayURL), &raw))
	require.NotContains(t, raw.Limitation, "created_at_lower_limit")
	require.JSONEq(t, "600", string(raw.Limitation["created_at_upper_limit"]))
	require.EqualValues(t, 600, info.Limitation.CreatedAtUpperLimit)
	require.Contains(t, info.PostingPolicy, "replaceable and addressable events and deletion requests are accepted at any age")
	require.False(t, info.Limitation.RestrictedWrites)
	require.Len(t, info.Retention, 2)
	require.Equal(t, [][]int{{1059, 1059}}, info.Retention[0].Kinds)
	require.EqualValues(t, 24*60*60, info.Retention[0].Time)

	require.NoError(t, server.policy.mutatePubkey(nostr.Generate().Public(), "test", true))
	require.True(t, fetchNIP11(t, relayURL).Limitation.RestrictedWrites)
	require.False(t, server.relay.Info.Limitation.RestrictedWrites, "the shared document is not mutated")
}

// countingSync runs SyncEventsFromIDs and counts the ids moved per direction.
func countingSync(up, down *atomic.Int64) func(context.Context, nip77.Direction) {
	return func(ctx context.Context, dir nip77.Direction) {
		counter := down
		if _, toRelay := dir.To.(*nostr.Relay); toRelay {
			counter = up
		}
		items := make(chan nostr.ID)
		counted := nip77.Direction{From: dir.From, To: dir.To, Items: items}
		go func() {
			defer close(items)
			for id := range dir.Items {
				counter.Add(1)
				items <- id
			}
		}()
		nip77.SyncEventsFromIDs(ctx, counted)
	}
}

// TestSidecarNegentropyReconcilesBothDirections covers both-direction reconciliation: a client holding
// a different subset reconciles against the sidecar with
// fiatjaf.com/nostr/nip77. Each side ends with the union, and a second
// session finds nothing missing either way.
func TestSidecarNegentropyReconcilesBothDirections(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), fanoutTestTimeout)
	defer cancel()
	server, relayURL := startSidecarForFanoutTest(t)
	wsURL := "ws" + strings.TrimPrefix(relayURL, "http")
	sk := nostr.Generate()
	filter := nostr.Filter{Kinds: []nostr.Kind{1, 30078}, Authors: []nostr.PubKey{sk.Public()}}

	local := wrappers.StorePublisher{Store: &slicestore.SliceStore{}, MaxLimit: 10_000}
	require.NoError(t, local.Init())
	var common, relayOnly, localOnly []nostr.Event
	for i := range 3 {
		common = append(common, signedNowEvent(t, sk, 1, i, nil, "common "+strconv.Itoa(i)))
	}
	for i := range 4 {
		relayOnly = append(relayOnly, signedNowEvent(t, sk, 1, 10+i, nil, "relay "+strconv.Itoa(i)))
	}
	for i := range 5 {
		localOnly = append(localOnly, signedNowEvent(t, sk, 1, 20+i, nil, "local "+strconv.Itoa(i)))
	}
	localOnly = append(localOnly, signedNowEvent(t, sk, 30078, 30, nostr.Tags{{"d", "state"}}, "local state"))
	for _, event := range slices.Concat(common, relayOnly) {
		_, err := server.Relay().AddEvent(ctx, event)
		require.NoError(t, err)
	}
	for _, event := range slices.Concat(common, localOnly) {
		require.NoError(t, local.Publish(ctx, event))
	}

	var up, down atomic.Int64
	require.NoError(t, nip77.NegentropySync(ctx, wsURL, filter, local, local, countingSync(&up, &down)))
	require.EqualValues(t, len(localOnly), up.Load(), "client-only events uploaded")
	require.EqualValues(t, len(relayOnly), down.Load(), "relay-only events downloaded")

	all := slices.Concat(common, relayOnly, localOnly)
	for _, event := range all {
		require.Equal(t, []nostr.ID{event.ID}, storeIDs(t, server.store, nostr.Filter{IDs: []nostr.ID{event.ID}}), "relay is missing %q", event.Content)
		var found bool
		for range local.QueryEvents(nostr.Filter{IDs: []nostr.ID{event.ID}}) {
			found = true
		}
		require.True(t, found, "client is missing %q", event.Content)
	}

	up.Store(0)
	down.Store(0)
	require.NoError(t, nip77.NegentropySync(ctx, wsURL, filter, local, local, countingSync(&up, &down)))
	require.Zero(t, up.Load())
	require.Zero(t, down.Load())
}

// TestSidecarNegentropyRefusesSetsLargerThanTheLimit: a NEG-OPEN whose filter
// matches more than negentropy_max_events is refused with NEG-ERR rather than
// reconciled against a truncated set.
func TestSidecarNegentropyRefusesSetsLargerThanTheLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), fanoutTestTimeout)
	defer cancel()
	cfg := sidecarTestConfig(t)
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.NegentropyMaxEvents = 2
	server, err := New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close()) })
	relayURL := startHTTPTestServer(t, server)
	sk := nostr.Generate()
	for i := range 3 {
		_, err := server.Relay().AddEvent(ctx, signedNowEvent(t, sk, 1, i, nil, strconv.Itoa(i)))
		require.NoError(t, err)
	}
	local := wrappers.StorePublisher{Store: &slicestore.SliceStore{}, MaxLimit: 10}
	require.NoError(t, local.Init())
	filter := nostr.Filter{Kinds: []nostr.Kind{1}, Authors: []nostr.PubKey{sk.Public()}}
	err = nip77.NegentropySync(ctx, "ws"+strings.TrimPrefix(relayURL, "http"), filter, local, local, nip77.SyncEventsFromIDs)
	require.ErrorContains(t, err, "more than the 2 this relay reconciles")

	filter.Limit = 2 // a bounded filter within the limit is served
	require.NoError(t, nip77.NegentropySync(ctx, "ws"+strings.TrimPrefix(relayURL, "http"), filter, local, local, nip77.SyncEventsFromIDs))
}

func TestSidecarMetricsReportRetentionDeletions(t *testing.T) {
	server, _ := startSidecarForFanoutTest(t)
	sk := nostr.Generate()
	require.NoError(t, server.store.Save(t.Context(), signedNowEvent(t, sk, kinds.ContextVMGiftWrap, 0, nil, "")))
	result, err := server.sweepRetention(t.Context(), time.Now().Add(48*time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 1, result.Request)
	var metrics strings.Builder
	require.NoError(t, server.WritePrometheus(&metrics))
	require.Contains(t, metrics.String(), `bahia_relay_sidecar_retention_deleted_events_total{cause="request_retention"} 1`)
	require.Contains(t, metrics.String(), `bahia_relay_sidecar_retention_deleted_events_total{cause="nip40_expired"} 0`)
}
