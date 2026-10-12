package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func secretStoreConfig(t *testing.T, key nostr.SecretKey) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret-store.key")
	require.NoError(t, os.WriteFile(path, []byte(key.Hex()+"\n"), 0o600))
	cfg := &config.Config{SecretStore: config.SecretStoreConfig{Backend: config.SecretStoreBackendSignet, Signet: config.SecretStoreSignetConfig{
		Pubkey: nostr.Generate().Public().Hex(), Relays: []string{"ws://127.0.0.1:1"}, ClientKeyFile: path, Owner: "bahia-vault", RequestTimeout: time.Second,
	}}}
	return cfg
}

func TestOpenSecretStoreRefusesTheServiceKey(t *testing.T) {
	service := nostr.Generate()
	_, err := openSecretStore(context.Background(), secretStoreConfig(t, service), service.Public().Hex(), nostrout.Or(nil), zap.NewNop())
	require.ErrorContains(t, err, "holds the service key")
}

func TestOpenSecretStoreRefusesEnvironmentKey(t *testing.T) {
	t.Setenv(secretStoreClientKeyEnv, nostr.Generate().Hex())
	_, err := openSecretStore(context.Background(), secretStoreConfig(t, nostr.Generate()), nostr.Generate().Public().Hex(), nostrout.Or(nil), zap.NewNop())
	require.ErrorContains(t, err, secretStoreClientKeyEnv)
}

func TestOpenSecretStoreUnconfiguredIsNil(t *testing.T) {
	store, err := openSecretStore(context.Background(), &config.Config{}, "", nostrout.Or(nil), zap.NewNop())
	require.NoError(t, err)
	require.Nil(t, store)
}

func TestSecretStoreHealthCheckFailsClosedWhileSignetIsUnreachable(t *testing.T) {
	cfg := secretStoreConfig(t, nostr.Generate())
	store, err := openSecretStore(context.Background(), cfg, nostr.Generate().Public().Hex(), nostrout.Or(nil), zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(store.Close)
	check := secretStoreHealthCheck(store)
	require.Equal(t, HealthStatusWarn, check().Status, "no round trip yet")
	runner := &secretStoreProbeRunner{store: store, timeout: cfg.SecretStore.Signet.RequestTimeout, logger: zap.NewNop()}
	require.NoError(t, runner.Run(context.Background()), "a failed probe is reported in health, not as a runner failure")
	got := check()
	require.Equal(t, HealthStatusWarn, got.Status)
	require.True(t, strings.Contains(got.Message, "fail closed"), got.Message)
	require.NotEmpty(t, got.Details["last_round_trip"])
}
