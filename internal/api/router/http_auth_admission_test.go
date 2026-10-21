package router_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/api/handlers"
	"github.com/openagentsinc/bahia/internal/api/router"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestHTTPAuthAdmissionBoundsUnauthorizedReplayWrites(t *testing.T) {
	ledger, err := localstore.OpenOutbox(filepath.Join(t.TempDir(), "outbox.bolt"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ledger.Close()) })
	cfg := config.Defaults()
	cfg.Auth.Enabled = true
	cfg.Auth.BootstrapOwnerPubkeys = []string{nostr.Generate().Public().Hex()}
	handler := router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{Config: cfg,
		AuthMiddleware: auth.MiddlewareConfig{Enabled: true, NIP98Validator: auth.NewNIP98Validator(auth.DefaultNIP98Config(), auth.NewDurableNIP98ReplayStore(ledger, nil))},
		MCP:            handlers.NewMCPHandler(newRouterTestMCPServer(zap.NewNop()), zap.NewNop())})
	attacker := nostr.Generate()
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	hash := sha256.Sum256(body)
	var last nostr.Event
	var firstID nostr.ID
	throttled := false
	// The bucket refills continuously while Schnorr verification/fsync runs.
	// Consume a bounded burst until refusal instead of assuming a frozen clock.
	for i := 0; i < 200; i++ {
		request := httptest.NewRequest(http.MethodPost, "http://bahia.test/mcp", bytes.NewReader(body))
		request.RemoteAddr = "192.0.2.9:12345"
		request.Header.Set("Content-Type", "application/json")
		last = nostr.Event{Kind: 27235, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"u", request.URL.String()}, {"method", http.MethodPost}, {"payload", hex.EncodeToString(hash[:])}, {"nonce", fmt.Sprint(i)}}}
		require.NoError(t, last.Sign(attacker))
		if i == 0 {
			firstID = last.ID
		}
		raw, err := json.Marshal(last)
		require.NoError(t, err)
		request.Header.Set("Authorization", "Nostr "+base64.StdEncoding.EncodeToString(raw))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code == http.StatusTooManyRequests {
			throttled = true
			break
		}
		require.Equal(t, http.StatusInternalServerError, response.Code, "request %d: %s", i, response.Body.String())
	}
	require.True(t, throttled, "valid but unauthorized requests must reach pre-auth admission refusal")
	alreadyConsumed, err := ledger.ClaimHTTPAuth(t.Context(), firstID.Hex(), time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.False(t, alreadyConsumed, "valid denied request passed authentication before platform refusal")
	unclaimed, err := ledger.ClaimHTTPAuth(t.Context(), last.ID.Hex(), time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.True(t, unclaimed, "throttled request must not reach the critical replay ledger")
}
