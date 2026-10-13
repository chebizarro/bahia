package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/repository/repositorytest"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/openagentsinc/bahia/internal/servicesigner"
	"github.com/stretchr/testify/require"
)

type configFabricCapturePublisher struct{ events []nostr.Event }

func (p *configFabricCapturePublisher) PublishPresignedEvent(_ context.Context, event nostr.Event, _ string) error {
	p.events = append(p.events, event)
	return nil
}

func configFabricTestRequest() service.ConfigPublishRequest {
	return service.ConfigPublishRequest{Kind: service.ConfigFabricPolicyKind, ServiceID: "relay", PolicyName: "limits", Scope: "prod", Version: 1, Schema: "cascadia.config.limits.v1", Policy: map[string]any{"max": 5.0}}
}

func TestConfigFabricSignerIndependentOfDisabledAssistant(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restoreDBHooks()
	operator := nostr.Generate()
	client := nostr.Generate()
	serviceIdentity := nostr.Generate()
	// The opener models a generic remote NIP-46 signer. The service-key opener
	// must not be used for config-fabric, and no assistant Signet is started.
	previous := openServiceSigner
	t.Cleanup(func() { openServiceSigner = previous })
	var opens int
	openServiceSigner = func(_ context.Context, signerCfg config.NostrConfig, _ servicesigner.Options) (nostr.Keyer, error) {
		if signerCfg.Signer.Method == "" {
			return nostrutil.NewLocalKeyer(signerCfg.PrivateKey)
		}
		opens++
		require.Equal(t, config.NostrSignerNIP46, signerCfg.Signer.Method)
		require.Equal(t, operator.Public().Hex(), signerCfg.PublicKey)
		return nostrutil.NewLocalKeyer(operator.Hex())
	}
	cfg := startupTestConfig("emergency")
	cfg.Assistant.Enabled = false
	cfg.Nostr.PrivateKey = serviceIdentity.Hex()
	cfg.Nostr.LocalStore.Path = filepath.Join(t.TempDir(), "events.bolt")
	cfg.ConfigFabric = config.ConfigFabricConfig{PublicKey: operator.Public().Hex(), Signer: config.NostrSignerConfig{Method: config.NostrSignerNIP46, BunkerURI: "bunker://" + operator.Public().Hex() + "?relay=wss://bunker.invalid", ClientSecretKey: client.Hex()}}
	application, err := New(cfg)
	require.NoError(t, err)
	defer syncTestLogger(t, application.Logger)
	defer closeRelayPools(application.relayPools...)
	defer application.closeConfigFabricSigner()
	require.Equal(t, 1, opens)
	require.NotNil(t, application.configFabricSignerSession)
	var health HealthCheck
	for _, check := range application.Health.Readiness().Checks {
		if check.Name == "config_fabric_signer" {
			health = check
		}
	}
	require.Equal(t, HealthStatusPass, health.Status)

	repo := repositorytest.NewInMemoryNostrEventRepository()
	publisher := &configFabricCapturePublisher{}
	fabric := service.NewConfigFabricService(repo, publisher, configFabricKeyer{application.configFabricSignerSession.Keyer()})
	published, err := fabric.Publish(t.Context(), configFabricTestRequest())
	require.NoError(t, err)
	rolledBack, err := fabric.Rollback(t.Context(), published.EventID)
	require.NoError(t, err)
	require.Equal(t, 2, rolledBack.Version)
	require.Len(t, publisher.events, 2)
	for _, event := range publisher.events {
		require.Equal(t, operator.Public(), event.PubKey)
		require.True(t, event.CheckID())
		require.True(t, event.VerifySignature())
	}
}

func TestConfigFabricSignerAbsentIsExplicitlyUnavailable(t *testing.T) {
	provider := NewHealthProvider(nil, nil)
	registerConfigFabricSignerHealth(provider, nil)
	var health HealthCheck
	for _, check := range provider.Readiness().Checks {
		if check.Name == "config_fabric_signer" {
			health = check
		}
	}
	require.Equal(t, HealthStatusWarn, health.Status)
	require.Contains(t, health.Message, "publish and rollback are unavailable")
	fabric := service.NewConfigFabricService(repositorytest.NewInMemoryNostrEventRepository(), &configFabricCapturePublisher{}, nil)
	_, err := fabric.Publish(t.Context(), configFabricTestRequest())
	require.ErrorContains(t, err, "operator signer is not configured")
}
