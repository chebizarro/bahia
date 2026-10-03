package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/relaysidecar"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestConfigCLIProducesOperatorSignedEventsAndRollsBackFromOutbox(t *testing.T) {
	resetNostrKeyGlobals(t)
	for _, name := range []string{"BAHIA_NOSTR_BUNKER_FILE", "BAHIA_NOSTR_BUNKER_URI", "BAHIA_NOSTR_CLIENT_KEY_FILE", "BAHIA_NOSTR_CLIENT_PRIVATE_KEY", "BAHIA_NOSTR_NSEC", "BAHIA_NOSTR_PRIVATE_KEY"} {
		t.Setenv(name, "")
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	serviceKey, operator := nostr.Generate(), nostr.Generate()
	cfg := config.Defaults().Nostr
	cfg.PrivateKey = serviceKey.Hex()
	cfg.Sidecar.DataDir = t.TempDir()
	cfg.Sidecar.ReadAuthMode = config.ReadAuthModeOff
	cfg.Sidecar.ServiceID = "relay-sidecar-test"
	cfg.Sidecar.Scope = "prod"
	cfg.Sidecar.ConfigProjectionPath = filepath.Join(t.TempDir(), "projection.json")
	cfg.Sidecar.ListenAddr = "127.0.0.1:0"
	cfg.AuthorizedPubkeys = []string{operator.Public().Hex()}
	relay, err := relaysidecar.New(cfg, nil)
	require.NoError(t, err)
	server := httptest.NewServer(relay.Handler())
	runCtx, stop := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- relay.Run(runCtx) }()
	t.Cleanup(func() {
		server.Close()
		stop()
		require.NoError(t, <-runDone)
	})
	relayURL := "ws" + strings.TrimPrefix(server.URL, "http")
	statusPool := nostrpool.NewRelayPool([]string{relayURL}, zap.NewNop())
	statusPool.Connect(t.Context())
	t.Cleanup(statusPool.Close)
	statusCtx, cancelStatus := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancelStatus)
	statusSub, err := statusPool.SubscribeAllWithEOSE(statusCtx, []nostr.Filter{{
		Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Authors: []nostr.PubKey{serviceKey.Public()},
		Tags: nostr.TagMap{"d": {"config-status:relay-sidecar-test:membership:prod"}},
	}})
	require.NoError(t, err)
	t.Cleanup(statusSub.Close)
	awaitApplied := func(id string) {
		t.Helper()
		for {
			select {
			case event, ok := <-statusSub.Events:
				require.True(t, ok, "config status subscription closed")
				if event != nil && event.Tags.Find("status")[1] == "applied" && event.Tags.Find("e")[1] == id {
					return
				}
			case <-statusCtx.Done():
				t.Fatalf("no applied config status for %s: %v", id, statusCtx.Err())
			}
		}
	}
	keyFile := filepath.Join(t.TempDir(), "operator.key")
	require.NoError(t, os.WriteFile(keyFile, []byte(operator.Hex()), 0o600))
	run := func(args ...string) {
		t.Helper()
		root := newRootCommand()
		root.SilenceUsage = true
		root.SetArgs(append([]string{"--relay", relayURL, "--service-pubkey", serviceKey.Public().Hex(), "--nostr-key-file", keyFile}, args...))
		require.NoError(t, root.ExecuteContext(t.Context()))
	}
	writeRequest := func(version int, member string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "request.json")
		body, err := json.Marshal(service.ConfigPublishRequest{
			Kind: kinds.ConfigACLList, ServiceID: "relay-sidecar-test", PolicyName: "membership", Scope: "prod",
			Version: version, Schema: "cascadia.config.membership.v1",
			Items: []service.ConfigListItem{{Tag: "p", Value: member}},
		})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, body, 0o600))
		return path
	}
	current := func() nostr.Event {
		t.Helper()
		var events []nostr.Event
		for event := range relay.Relay().QueryStored(t.Context(), nostr.Filter{
			Kinds: []nostr.Kind{nostr.Kind(kinds.ConfigACLList)}, Authors: []nostr.PubKey{operator.Public()},
		}) {
			events = append(events, event)
		}
		require.Len(t, events, 1)
		return events[0]
	}
	run("config", "publish", "--file", writeRequest(1, operator.Public().Hex()))
	first := current()
	awaitApplied(first.ID.Hex())
	outbox, err := localstore.OpenOutbox(cliOutboxDefaultPath())
	require.NoError(t, err)
	entry, found, err := outbox.Get(first.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, entry.Relays[relayURL].Accepted, "relay OK must be recorded in CLI outbox")
	require.NoError(t, outbox.Close())
	require.True(t, first.CheckID())
	require.True(t, first.VerifySignature())
	require.Equal(t, operator.Public(), first.PubKey)
	run("config", "publish", "--file", writeRequest(2, nostr.Generate().Public().Hex()))
	second := current()
	require.NotEqual(t, first.ID, second.ID)
	require.Greater(t, second.CreatedAt, first.CreatedAt)
	awaitApplied(second.ID.Hex())
	run("config", "rollback", first.ID.Hex())
	rolled := current()
	awaitApplied(rolled.ID.Hex())
	require.NotEqual(t, first.ID, rolled.ID)
	require.Greater(t, rolled.CreatedAt, second.CreatedAt)
	request, err := service.ConfigRequestFromEvent(rolled)
	require.NoError(t, err)
	require.Equal(t, 3, request.Version)
	require.Equal(t, operator.Public().Hex(), request.Items[0].Value)
	local, err := service.ConfigDriftFromEvents([]nostr.Event{rolled})
	require.NoError(t, err)
	require.True(t, local[0].Drift, "desired state without observation must drift")
	for event := range relay.Relay().QueryStored(t.Context(), nostr.Filter{
		Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Authors: []nostr.PubKey{serviceKey.Public()},
		Tags: nostr.TagMap{"d": {"config-status:relay-sidecar-test:membership:prod"}},
	}) {
		local, err = service.ConfigDriftFromEvents([]nostr.Event{rolled, event})
		require.NoError(t, err)
		require.False(t, local[0].Drift, "applied status must clear local drift")
	}
}
