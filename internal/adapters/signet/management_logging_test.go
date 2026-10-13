package signet

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/stretchr/testify/require"
)

// TestSignetManagementPoolLogsThroughTheClientLogger: the management pool's
// relay diagnostics reach the client's zap logger instead of a no-op
// logger. A management relay that refuses connections makes
// the pool log the failed subscription before SubscribeAllWithEOSE returns.
func TestSignetManagementPoolLogsThroughTheClientLogger(t *testing.T) {
	server := httptest.NewServer(nil)
	relayURL := "ws" + strings.TrimPrefix(server.URL, "http")
	server.Close() // nothing listens there any more

	var logs bytes.Buffer
	encoder := zap.NewProductionEncoderConfig()
	encoder.EncodeLevel = zapcore.CapitalLevelEncoder
	logger := zap.New(zapcore.NewCore(zapcore.NewJSONEncoder(encoder), zapcore.AddSync(&logs), zapcore.DebugLevel))
	client, err := NewClient(Config{Relays: []string{relayURL}}, logger)
	require.NoError(t, err)
	pool := client.newManagementPool((*nostrout.Bunker)(nil))
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
