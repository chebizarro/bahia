package app

import (
	"context"
	"sync"

	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"go.uber.org/zap"
)

type relayTopologyCoordinator struct {
	mu               sync.Mutex
	controlPlanePool *nostrAdapter.RelayPool
	responsePool     *nostrAdapter.RelayPool
	servicePool      *nostrAdapter.RelayPool
	nostrConfig      config.NostrConfig
	loomRelays       []string
	logger           *zap.Logger
}

type relayTopologyCoordinatorConfig struct {
	ControlPlanePool *nostrAdapter.RelayPool
	ResponsePool     *nostrAdapter.RelayPool
	ServicePool      *nostrAdapter.RelayPool
	NostrConfig      config.NostrConfig
	LoomRelays       []string
	Logger           *zap.Logger
}

func newRelayTopologyCoordinator(cfg relayTopologyCoordinatorConfig) *relayTopologyCoordinator {
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &relayTopologyCoordinator{
		controlPlanePool: cfg.ControlPlanePool,
		responsePool:     cfg.ResponsePool,
		servicePool:      cfg.ServicePool,
		nostrConfig:      cloneNostrRelayTopologyConfig(cfg.NostrConfig),
		loomRelays:       cloneAppStrings(cfg.LoomRelays),
		logger:           logger.Named("relay-topology-coordinator"),
	}
}

func (c *relayTopologyCoordinator) ApplySnapshot(ctx context.Context, state controlplane.RelayPolicyState) error {
	_ = ctx
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if !relayPolicySnapshotHasRuntimeTopology(state) {
		c.logger.Info("relay topology snapshot has no runtime relay topology; retaining existing relay pools")
		return nil
	}

	controlPlaneRelays, reconfigureControlPlane := c.controlPlaneRelaysForSnapshot(state)
	serviceRelays, reconfigureService := c.serviceRelaysForSnapshot(state, controlPlaneRelays)

	if reconfigureControlPlane && c.controlPlanePool != nil {
		result := c.controlPlanePool.ReconfigureRelayURLs(controlPlaneRelays)
		c.logReconfigureResult("control_plane", result)
	}
	if reconfigureControlPlane && c.responsePool != nil {
		result := c.responsePool.ReconfigureRelayURLs(controlPlaneRelays)
		c.logReconfigureResult("contextvm_response", result)
	}
	if reconfigureService && c.servicePool != nil {
		result := c.servicePool.ReconfigureRelayURLs(serviceRelays)
		c.logReconfigureResult("service_interop", result)
	}
	return nil
}

func (c *relayTopologyCoordinator) controlPlaneRelaysForSnapshot(state controlplane.RelayPolicyState) ([]string, bool) {
	if sidecarRelays := c.configuredSidecarRelays(); len(sidecarRelays) > 0 {
		return sidecarRelays, true
	}
	if len(state.ContextVMRelays) > 0 {
		return cloneAppStrings(state.ContextVMRelays), true
	}
	if len(state.ServiceRelays) > 0 {
		return cloneAppStrings(state.ServiceRelays), true
	}
	return nil, false
}

func (c *relayTopologyCoordinator) serviceRelaysForSnapshot(state controlplane.RelayPolicyState, controlPlaneRelays []string) ([]string, bool) {
	if c.nostrConfig.Sidecar.Enabled && c.nostrConfig.Sidecar.MirrorExternal {
		base := controlPlaneRelays
		if len(base) == 0 {
			base = c.configuredSidecarRelays()
		}
		if len(base) == 0 {
			return nil, false
		}
		return appendUniqueRelays(cloneAppStrings(base), c.loomRelays...), true
	}
	if len(state.ServiceRelays) == 0 {
		return nil, false
	}
	return appendUniqueRelays(cloneAppStrings(state.ServiceRelays), c.loomRelays...), true
}

func (c *relayTopologyCoordinator) configuredSidecarRelays() []string {
	if !c.nostrConfig.Sidecar.Enabled {
		return nil
	}
	if c.nostrConfig.Sidecar.BackendURL != "" {
		return []string{c.nostrConfig.Sidecar.BackendURL}
	}
	if c.nostrConfig.Sidecar.PublicURL != "" {
		return []string{c.nostrConfig.Sidecar.PublicURL}
	}
	return nil
}

func (c *relayTopologyCoordinator) logReconfigureResult(poolName string, result nostrAdapter.RelayPoolReconfigureResult) {
	if !result.Changed {
		c.logger.Debug("relay pool topology already converged", zap.String("pool", poolName), zap.Strings("relays", result.CurrentURLs))
		return
	}
	c.logger.Info("relay pool topology reconfigured from canonical relay policy", zap.String("pool", poolName), zap.Strings("previous_relays", result.PreviousURLs), zap.Strings("current_relays", result.CurrentURLs), zap.Strings("added_relays", result.AddedURLs), zap.Strings("removed_relays", result.RemovedURLs))
}

func relayPolicySnapshotHasRuntimeTopology(state controlplane.RelayPolicyState) bool {
	return len(state.BrowserRelays)+len(state.ContextVMRelays)+len(state.ServiceRelays) > 0
}

func cloneNostrRelayTopologyConfig(cfg config.NostrConfig) config.NostrConfig {
	cfg.Relays = cloneAppStrings(cfg.Relays)
	cfg.ServiceRelays = cloneAppStrings(cfg.ServiceRelays)
	cfg.BrowserRelays = cloneAppStrings(cfg.BrowserRelays)
	cfg.ContextVMRelays = cloneAppStrings(cfg.ContextVMRelays)
	return cfg
}

func appendUniqueRelays(relays []string, values ...string) []string {
	for _, relay := range values {
		relays = appendUniqueRelay(relays, relay)
	}
	return relays
}

func cloneAppStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}
