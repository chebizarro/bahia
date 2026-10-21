package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/stretchr/testify/require"
)

func TestDBLessDaemonMCPUsesRealOutboxWithAuthenticatedOperator(t *testing.T) {
	cfg := unreachableDatabaseConfig(t, "full")
	operator := nostr.Generate()
	cfg.Auth.Enabled = true
	cfg.Auth.BootstrapOwnerPubkeys = []string{operator.Public().Hex()}
	cfg.Nostr.AuthorizedPubkeys = []string{operator.Public().Hex()}
	application, err := New(cfg)
	require.NoError(t, err)
	require.Nil(t, application.DB)
	t.Cleanup(func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		require.NoError(t, application.RunContext(ctx))
	})
	pending := nostr.Event{Kind: 1, CreatedAt: nostr.Now(), Content: "durable but not yet relayed"}
	require.NoError(t, pending.Sign(nostr.Generate()))
	_, err = application.localOutbox.Enqueue(localstore.OutboxEntry{Event: pending, Target: "default"})
	require.NoError(t, err)
	counts, err := application.localOutbox.Counts()
	require.NoError(t, err)
	require.Positive(t, counts.Pending)
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"bahia_outbox_status","arguments":{}}}`)
	req := httptest.NewRequest(http.MethodPost, "http://bahia.test/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	hash := sha256.Sum256(body)
	authorization := nostr.Event{Kind: 27235, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"u", req.URL.String()}, {"method", http.MethodPost}, {"payload", hex.EncodeToString(hash[:])}}}
	require.NoError(t, authorization.Sign(operator))
	raw, err := json.Marshal(authorization)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Nostr "+base64.StdEncoding.EncodeToString(raw))
	response := httptest.NewRecorder()
	application.HTTPServer.Handler.ServeHTTP(response, req)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var rpc struct {
		Error  json.RawMessage `json:"error"`
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &rpc))
	require.Empty(t, rpc.Error)
	require.False(t, rpc.Result.IsError, response.Body.String())
	require.Len(t, rpc.Result.Content, 1)
	var actual struct {
		Pending int `json:"pending"`
		Failed  int `json:"failed"`
	}
	require.NoError(t, json.Unmarshal([]byte(rpc.Result.Content[0].Text), &actual))
	require.EqualValues(t, counts.Pending, actual.Pending)
	require.EqualValues(t, counts.Failed, actual.Failed)
	replay := httptest.NewRequest(http.MethodPost, "http://bahia.test/mcp", bytes.NewReader(body))
	replay.Header = req.Header.Clone()
	denied := httptest.NewRecorder()
	application.HTTPServer.Handler.ServeHTTP(denied, replay)
	require.Equal(t, http.StatusUnauthorized, denied.Code, "same signed HTTP request must not be reusable")
}
