package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/openagentsinc/bahia/internal/servicesigner"
	"go.uber.org/zap"
)

// configFabricKeyer adapts the standard signer without exposing custody details
// to the config-fabric service.
type configFabricKeyer struct{ nostr.Keyer }

var _ service.ConfigFabricSigner = configFabricKeyer{}

func (s configFabricKeyer) GetPublicKey(ctx context.Context) (string, error) {
	pubkey, err := s.Keyer.GetPublicKey(ctx)
	if err != nil {
		return "", err
	}
	return pubkey.Hex(), nil
}

func (s configFabricKeyer) Sign(ctx context.Context, event *nostr.Event) error {
	return s.SignEvent(ctx, event)
}

func newConfigFabricKeyer(cfg config.ConfigFabricConfig, admission *nostrout.Admission, logger *zap.Logger, running *serviceSigner) (*serviceSigner, func(), error) {
	if strings.TrimSpace(cfg.PublicKey) == "" && cfg.Signer == (config.NostrSignerConfig{}) {
		return nil, func() {}, nil
	}
	method := strings.ToLower(strings.TrimSpace(cfg.Signer.Method))
	if method != config.NostrSignerNIP46 && method != config.NostrSignerNIP55L {
		return nil, nil, fmt.Errorf("config_fabric.signer.method must be nip46 or nip55l")
	}
	return newServiceKeyer(&config.Config{Nostr: config.NostrConfig{PublicKey: cfg.PublicKey, Signer: cfg.Signer}}, admission, logger, running)
}

func registerConfigFabricSignerHealth(provider *HealthProvider, session *serviceSigner) {
	provider.RegisterCheck("config_fabric_signer", func() HealthCheck {
		if session == nil || session.Keyer() == nil {
			return HealthCheck{Name: "config_fabric_signer", Status: HealthStatusWarn, Message: "config-fabric operator signer is not configured; publish and rollback are unavailable"}
		}
		probeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := servicesigner.CheckHealth(probeCtx, session.Keyer()); err != nil {
			return HealthCheck{Name: "config_fabric_signer", Status: HealthStatusWarn, Message: "config-fabric operator signer is unavailable: " + err.Error()}
		}
		return HealthCheck{Name: "config_fabric_signer", Status: HealthStatusPass, Message: "config-fabric operator signer is available"}
	})
}
