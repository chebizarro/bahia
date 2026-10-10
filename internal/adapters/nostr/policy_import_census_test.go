package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestPolicyCoordinateCensusRelayWinsWithColdCache(t *testing.T) {
	first := startSyncTestRelay(t, syncTestRelayOptions{})
	second := startSyncTestRelay(t, syncTestRelayOptions{})
	pool := newSyncTestPool(first, second)
	defer pool.Close()
	key := gonostr.Generate()
	id := uuid.New()
	ev := syncTestEvent(t, key, gonostr.Kind(kinds.CASControlState), gonostr.Now(), gonostr.Tags{
		{"d", id.String()}, {"t", kinds.CPStateTopicPolicyRegistry}, {"domain", "policy"},
	}, `{"id":"relay-version"}`)
	second.add(t, ev)
	ctx, cancel := context.WithTimeout(t.Context(), syncTestTimeout)
	defer cancel()
	result, err := CensusPolicyCoordinate(ctx, pool, key.Public(), id)
	require.NoError(t, err)
	require.Equal(t, []string{ev.ID.Hex()}, result.EventIDs, "a cold local cache cannot override a relay-held coordinate")
	require.Len(t, result.Relays, 2)
	_, err = CensusPolicyCoordinate(ctx, pool, key.Public(), uuid.New())
	require.ErrorContains(t, err, "absence is unprovable")
}

func TestPolicyCoordinateCensusRefusesIncompleteRelayHistory(t *testing.T) {
	key := gonostr.Generate()
	id := uuid.New()
	for name, configure := range map[string]func(*syncTestRelay){
		"closed": func(r *syncTestRelay) {
			r.relay.OnRequest = func(context.Context, gonostr.Filter) (bool, string) {
				return true, "blocked: denied"
			}
		},
		"unavailable": func(r *syncTestRelay) { r.down.Store(true) },
	} {
		t.Run(name, func(t *testing.T) {
			up := startSyncTestRelay(t, syncTestRelayOptions{})
			bad := startSyncTestRelay(t, syncTestRelayOptions{})
			configure(bad)
			pool := newSyncTestPool(up, bad)
			defer pool.Close()
			ctx, cancel := context.WithTimeout(t.Context(), syncTestTimeout)
			defer cancel()
			_, err := CensusPolicyCoordinate(ctx, pool, key.Public(), id)
			require.Error(t, err)
		})
	}
}

// A malicious relay can send an invalid signed EVENT and then EOSE. The
// fiatjaf transport discards that frame before subscribers can inspect it;
// this census must not turn that stream into a certified absence.
func TestPolicyCoordinateCensusCannotCertifyBadSignatureAsAbsent(t *testing.T) {
	key := gonostr.Generate()
	id := uuid.New()
	ev := syncTestEvent(t, key, gonostr.Kind(kinds.CASControlState), gonostr.Now(), gonostr.Tags{
		{"d", id.String()}, {"t", kinds.CPStateTopicPolicyRegistry}, {"domain", "policy"},
	}, `{"id":"malicious"}`)
	ev.Sig[0] ^= 1
	payload, err := json.Marshal(ev)
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			_, raw, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var frame []json.RawMessage
			if json.Unmarshal(raw, &frame) != nil || len(frame) < 2 {
				continue
			}
			var verb, subID string
			_ = json.Unmarshal(frame[0], &verb)
			_ = json.Unmarshal(frame[1], &subID)
			if verb != "REQ" {
				continue
			}
			if err := conn.Write(r.Context(), websocket.MessageText, []byte(fmt.Sprintf(`["EVENT",%q,%s]`, subID, payload))); err != nil {
				return
			}
			if err := conn.Write(r.Context(), websocket.MessageText, []byte(fmt.Sprintf(`["EOSE",%q]`, subID))); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	url := gonostr.NormalizeURL("ws" + strings.TrimPrefix(server.URL, "http"))
	pool := NewRelayPool([]string{url}, zap.NewNop())
	defer pool.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_, err = CensusPolicyCoordinate(ctx, pool, key.Public(), id)
	require.ErrorContains(t, err, "absence is unprovable")
}
