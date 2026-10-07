package signet

import (
	"bytes"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip46"
	"github.com/stretchr/testify/require"
)

// TestSignetManagementPoolLogsThroughTheClientLogger: the management pool's
// relay diagnostics reach the client's slog logger instead of a no-op zap
// logger (bahia-irsry.49). A management relay that refuses connections makes
// the pool log the failed subscription before SubscribeAllWithEOSE returns.
func TestSignetManagementPoolLogsThroughTheClientLogger(t *testing.T) {
	server := httptest.NewServer(nil)
	relayURL := "ws" + strings.TrimPrefix(server.URL, "http")
	server.Close() // nothing listens there any more

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client, err := NewClient(Config{Relays: []string{relayURL}}, logger)
	require.NoError(t, err)
	pool := client.newManagementPool((*nip46.BunkerClient)(nil))
	require.NotNil(t, pool)
	defer pool.Close()

	_, err = pool.SubscribeAllWithEOSE(t.Context(), []nostr.Filter{{Kinds: []nostr.Kind{nostr.KindGiftWrap}}})
	require.Error(t, err)
	out := logs.String()
	require.Contains(t, out, `"msg":"subscription failed"`)
	require.Contains(t, out, `"level":"WARN"`)
	require.Contains(t, out, `"component":"signet"`)
	require.Contains(t, out, `"relay_pool":"signet-management"`)
	require.Contains(t, out, `"relay":"`+nostr.NormalizeURL(relayURL)+`"`)
}
