package controlplane

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
	"github.com/openagentsinc/bahia/internal/adapters/nostr/relayadmin"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/relaysidecar"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestIntentAuthorsSyncerPushesToSidecar verifies the end-to-end flow:
//   - A TrustSet with a bootstrap owner
//   - An in-process sidecar with an admin endpoint and restricted writes
//   - The syncer pushes the bootstrap owner's pubkey to the sidecar
//   - The owner's intent event is accepted by the sidecar
//   - A non-member's intent event is blocked
func TestIntentAuthorsSyncerPushesToSidecar(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Generate keys.
	adminKey := nostr.Generate()    // sidecar administrator (NIP-86 auth)
	memberKey := nostr.Generate()   // bootstrap owner (intent author)
	outsiderKey := nostr.Generate() // not a member

	// Pre-seed the admin policy file with a restrictive allowlist (only the
	// admin pubkey may write). This avoids needing to call AllowPubkey, which
	// is an unwired export.
	dataDir := t.TempDir()
	policyPath := filepath.Join(dataDir, "relay-admin-policy.json")
	policy := map[string]any{
		"version":        1,
		"administrators": []string{adminKey.Public().Hex()},
		"allowed_pubkeys": []map[string]string{
			{"pubkey": adminKey.Public().Hex(), "reason": "admin"},
		},
		"banned_pubkeys":      []any{},
		"metadata":            map[string]string{"name": "test", "description": "test"},
		"used_authorizations": map[string]any{},
		"applied_config":      map[string]any{},
	}
	policyJSON, err := json.MarshalIndent(policy, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(policyPath, policyJSON, 0o600))

	// Start sidecar with restricted writes.
	sidecarCfg := config.Defaults().Nostr
	sidecarCfg.Sidecar.DataDir = dataDir
	sidecarCfg.Sidecar.Enabled = true
	sidecarCfg.Sidecar.PublicURL = "ws://localhost:3334"
	sidecarCfg.Sidecar.AdministratorPubkeys = []string{adminKey.Public().Hex()}
	sidecarCfg.Sidecar.AdminPolicyPath = policyPath
	sidecar, err := relaysidecar.New(sidecarCfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = sidecar.Close() })

	httpServer := httptest.NewServer(sidecar.Handler())
	t.Cleanup(httpServer.Close)

	// Build the NIP-86 client that the syncer will use.
	adminClient, err := relayadmin.NewClient(relayadmin.Config{
		Enabled:       true,
		PrivateKeyHex: adminKey.Hex(),
		Targets: []relayadmin.Target{{
			Ref:                  "test-sidecar",
			RelayURL:             "ws://localhost:3334",
			HTTPURL:              httpServer.URL,
			AdministratorPubkeys: []string{adminKey.Public().Hex()},
		}},
	})
	require.NoError(t, err)

	// Create TrustSet with the member as a bootstrap owner.
	orgID := "00000000-0000-0000-0000-000000000001"
	trustSet := NewTrustSet(nil, zap.NewNop(), WithBootstrapOwners(map[string]string{
		orgID: memberKey.Public().Hex(),
	}))

	// Create and run the syncer.
	syncer := NewIntentAuthorsSyncer(IntentAuthorsSyncerConfig{
		TrustSet:   trustSet,
		Admin:      adminClient,
		TargetRefs: []string{"test-sidecar"},
		Logger:     zap.NewNop(),
	})

	// Run the syncer briefly to trigger the initial push.
	syncCtx, syncCancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = syncer.Run(syncCtx)
	}()
	// Give the syncer time to push.
	time.Sleep(200 * time.Millisecond)

	// Connect via WebSocket.
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http")
	relay, err := nostr.RelayConnect(ctx, wsURL, nostr.RelayOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = relay.Close() })

	// Test 1: member's intent event is accepted after sync.
	memberIntent := nostr.Event{
		Kind:      30900,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", "svc-sync-test"},
			{"t", "bahia-intent"},
			{"domain", "service"},
			{"op", "create"},
			{"org", orgID},
			{"intent_id", "sync-test-001"},
		},
		Content: `{"name":"synced-service"}`,
	}
	require.NoError(t, memberIntent.Sign(memberKey))
	err = relay.Publish(ctx, memberIntent)
	require.NoError(t, err, "bootstrap owner's intent should be accepted after syncer push")

	// Test 2: outsider's intent event is blocked.
	outsiderIntent := nostr.Event{
		Kind:      30900,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", "svc-outsider-test"},
			{"t", "bahia-intent"},
			{"domain", "service"},
			{"op", "create"},
			{"org", orgID},
			{"intent_id", "outsider-test-001"},
		},
		Content: `{"name":"outsider-service"}`,
	}
	require.NoError(t, outsiderIntent.Sign(outsiderKey))
	err = relay.Publish(ctx, outsiderIntent)
	require.Error(t, err, "non-member's intent should be blocked")

	// Cleanup: stop syncer.
	syncCancel()
	<-done
}
