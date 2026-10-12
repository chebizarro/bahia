package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/relayguard"
	"go.uber.org/zap"
)

const relayTopologyApplyTimeout = 30 * time.Second

// relayTopologyCatchUpWarnAfter bounds how long relay_topology health stays
// pass while the canonical relay settings have not caught up (no EOSE from
// every relay): past it the check warns, because nothing is advertised until
// catch-up. It is a health bound, not a delivery timer.
const relayTopologyCatchUpWarnAfter = 5 * time.Minute

// Sources of the relay topology in force.
const (
	// relayTopologySourceBootstrap: the static nostr relay lists, in force
	// until the canonical relay settings are received.
	relayTopologySourceBootstrap = "bootstrap"
	// relayTopologySourceCanonical: the canonical relay settings govern.
	relayTopologySourceCanonical = "canonical"
)

// errRelayPolicyRefused marks canonical relay settings the daemon refuses
// (a relay outside nostr.controlled_relays, a public relay, or fewer service
// write relays than nostr.publish_quorum). The topology in force is kept.
var errRelayPolicyRefused = errors.New("canonical relay settings refused")

// relayTopologyCoordinator owns Bahia's effective relay policy: the static
// nostr relay lists until the canonical relay settings are received, then the
// canonical lists. Every consumer derives from it: the relay pools, the
// service publisher's write set (and so its quorum accounting), the advertised
// relay-set and NIP-65 events, and the relay_topology health check.
type relayTopologyCoordinator struct {
	// applyMu serializes snapshot application (pool reconfiguration, which
	// may connect relays); mu guards the state below and is never held
	// across relay I/O, so publishers and health checks never wait on it.
	applyMu               sync.Mutex
	mu                    sync.Mutex
	controlPlanePool      *nostrAdapter.RelayPool
	contextVMRequestPool  *nostrAdapter.RelayPool
	contextVMResponsePool *nostrAdapter.RelayPool
	servicePool           *nostrAdapter.RelayPool
	nostrConfig           config.NostrConfig
	loomRelays            []string
	logger                *zap.Logger
	advertise             func(context.Context) error
	now                   func() time.Time
	startedAt             time.Time

	source       string
	effective    config.ControlPlaneRelays
	nip34        []string
	serviceWrite []string
	refused      string
	caughtUp     bool
}

type relayTopologyCoordinatorConfig struct {
	ControlPlanePool      *nostrAdapter.RelayPool
	ContextVMRequestPool  *nostrAdapter.RelayPool
	ContextVMResponsePool *nostrAdapter.RelayPool
	ServicePool           *nostrAdapter.RelayPool
	NostrConfig           config.NostrConfig
	LoomRelays            []string
	Logger                *zap.Logger
	Now                   func() time.Time
}

func newRelayTopologyCoordinator(cfg relayTopologyCoordinatorConfig) *relayTopologyCoordinator {
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	nostrConfig := cloneNostrRelayTopologyConfig(cfg.NostrConfig)
	static := staticControlPlaneRelays(nostrConfig)
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &relayTopologyCoordinator{
		now:                   now,
		startedAt:             now(),
		controlPlanePool:      cfg.ControlPlanePool,
		contextVMRequestPool:  cfg.ContextVMRequestPool,
		contextVMResponsePool: cfg.ContextVMResponsePool,
		servicePool:           cfg.ServicePool,
		nostrConfig:           nostrConfig,
		loomRelays:            cloneAppStrings(cfg.LoomRelays),
		logger:                logger.Named("relay-topology-coordinator"),
		source:                relayTopologySourceBootstrap,
		effective:             static,
		nip34:                 nostrConfig.NIP34RelayPolicyRelays(),
		serviceWrite:          nostrConfig.ServiceWriteRelays(static.Service),
	}
}

func staticControlPlaneRelays(cfg config.NostrConfig) config.ControlPlaneRelays {
	return config.ControlPlaneRelays{
		Service:   cfg.ServiceRelayPolicyRelays(),
		ContextVM: cfg.ContextVMRelayPolicyRelays(),
		Browser:   cfg.BrowserRelayPolicyRelays(),
	}
}

// serviceRelayPoolURLs is the service pool's relay set: the service write
// relays for serviceRelays plus the Loom relays, which share the pool for
// Loom traffic but are never service write relays.
func serviceRelayPoolURLs(cfg config.NostrConfig, serviceRelays, loomRelays []string) []string {
	return appendUniqueRelays(cfg.ServiceWriteRelays(serviceRelays), loomRelays...)
}

// SetAdvertiser registers the publisher of the advertised relay events. It
// runs when the canonical relay settings first catch up and on every applied
// change after that.
func (c *relayTopologyCoordinator) SetAdvertiser(advertise func(context.Context) error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.advertise = advertise
}

// ServiceWriteRelays is the service publisher's write set in force.
func (c *relayTopologyCoordinator) ServiceWriteRelays() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneAppStrings(c.serviceWrite)
}

// AdvertisedRelays implements nostr.AdvertisedRelaySource: the relay lists in
// force, known once the canonical relay settings have caught up (with no
// canonical settings on the relays, the bootstrap lists are advertised).
func (c *relayTopologyCoordinator) AdvertisedRelays() (nostrAdapter.AdvertisedRelays, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.caughtUp {
		return nostrAdapter.AdvertisedRelays{}, false
	}
	return nostrAdapter.AdvertisedRelays{
		Browser:   cloneAppStrings(c.effective.Browser),
		ContextVM: cloneAppStrings(c.effective.ContextVM),
		Service:   cloneAppStrings(c.effective.Service),
		NIP34:     cloneAppStrings(c.nip34),
	}, true
}

// MarkCaughtUp records that the canonical relay settings caught up (EOSE)
// and advertises the relay lists in force.
func (c *relayTopologyCoordinator) MarkCaughtUp(ctx context.Context) {
	c.mu.Lock()
	c.caughtUp = true
	advertise := c.advertise
	c.mu.Unlock()
	c.runAdvertiser(ctx, advertise)
}

// ValidateSnapshot is the relay settings hydrator's validator: it refuses
// canonical relay settings that fail controlplane.ValidateRelayPolicy before
// they are promoted, records the refusal for health and returns an error
// wrapping errRelayPolicyRefused.
func (c *relayTopologyCoordinator) ValidateSnapshot(state controlplane.RelayPolicyState) error {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	return c.validateLocked(state)
}

// validateLocked validates state and records a refusal. Callers hold applyMu.
func (c *relayTopologyCoordinator) validateLocked(state controlplane.RelayPolicyState) error {
	err := controlplane.ValidateRelayPolicy(c.nostrConfig, state)
	if err == nil {
		return nil
	}
	c.mu.Lock()
	c.refused = err.Error()
	current := cloneAppStrings(c.effective.Service)
	source := c.source
	c.mu.Unlock()
	c.logger.Error("refusing canonical relay settings; the relay topology in force is kept",
		zap.Error(err),
		zap.String("source", source),
		zap.Strings("service_relays", current),
	)
	return fmt.Errorf("%w: %v", errRelayPolicyRefused, err)
}

// ApplySnapshot applies validated canonical relay settings. Each non-empty
// canonical list governs (controlplane.EffectiveControlPlaneRelays). Settings
// that fail ValidateSnapshot are refused: nothing changes, the refusal is
// logged and reported by health, and the returned error wraps
// errRelayPolicyRefused.
func (c *relayTopologyCoordinator) ApplySnapshot(ctx context.Context, state controlplane.RelayPolicyState) error {
	if c == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	if err := c.validateLocked(state); err != nil {
		return err
	}
	effective := controlplane.EffectiveControlPlaneRelays(c.nostrConfig, state)
	applyCtx, cancel := context.WithTimeout(ctx, relayTopologyApplyTimeout)
	if relayPolicySnapshotHasRuntimeTopology(state) {
		c.reconfigurePools(applyCtx, state)
	}
	cancel()

	c.mu.Lock()
	if len(state.ServiceRelays) > 0 {
		c.serviceWrite = c.nostrConfig.ServiceWriteRelays(state.ServiceRelays)
	}
	c.effective = effective
	if len(state.NIP34Relays) > 0 {
		c.nip34 = cloneAppStrings(state.NIP34Relays)
	} else {
		c.nip34 = c.nostrConfig.NIP34RelayPolicyRelays()
	}
	c.source = relayTopologySourceCanonical
	c.refused = ""
	drift := c.driftLocked()
	fields := []zap.Field{
		zap.Strings("service_relays", c.effective.Service),
		zap.Strings("service_write_relays", c.serviceWrite),
		zap.Strings("contextvm_relays", c.effective.ContextVM),
		zap.Strings("browser_relays", c.effective.Browser),
		zap.Int("publish_quorum", c.nostrConfig.PublishQuorum),
	}
	advertise := c.advertise
	if !c.caughtUp {
		advertise = nil
	}
	c.mu.Unlock()

	c.logger.Info("canonical relay settings govern the relay topology", fields...)
	if len(drift) > 0 {
		c.logger.Warn("static nostr relay configuration differs from the canonical relay settings; the static lists are bootstrap only",
			zap.Strings("drifted_lists", drift),
			zap.Strings("static_service_relays", c.nostrConfig.ServiceRelayPolicyRelays()),
		)
	}
	c.runAdvertiser(ctx, advertise)
	return nil
}

func (c *relayTopologyCoordinator) runAdvertiser(ctx context.Context, advertise func(context.Context) error) {
	if advertise == nil {
		return
	}
	if err := advertise(ctx); err != nil {
		c.logger.Warn("publishing the advertised relay lists failed", zap.Error(err))
	}
}

// reconfigurePools moves the pools to the lists the canonical settings
// provide; a pool whose list the settings leave empty keeps its relays.
func (c *relayTopologyCoordinator) reconfigurePools(ctx context.Context, state controlplane.RelayPolicyState) {
	controlPlaneRelays, reconfigureControlPlane := c.controlPlaneRelaysForSnapshot(state)
	contextVMRelays, reconfigureContextVM := c.contextVMRelaysForSnapshot(state)
	serviceRelays, reconfigureService := c.serviceRelaysForSnapshot(state)

	if reconfigureControlPlane && c.controlPlanePool != nil {
		c.logReconfigureResult("control_plane", c.controlPlanePool.ReconfigureRelayURLsContext(ctx, controlPlaneRelays))
	}
	if reconfigureContextVM && c.contextVMRequestPool != nil {
		c.logReconfigureResult("contextvm_request", c.contextVMRequestPool.ReconfigureRelayURLsContext(ctx, contextVMRelays))
	}
	if reconfigureContextVM && c.contextVMResponsePool != nil {
		c.logReconfigureResult("contextvm_response", c.contextVMResponsePool.ReconfigureRelayURLsContext(ctx, contextVMRelays))
	}
	if reconfigureService && c.servicePool != nil {
		c.logReconfigureResult("service_interop", c.servicePool.ReconfigureRelayURLsContext(ctx, serviceRelays))
	}
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

func (c *relayTopologyCoordinator) contextVMRelaysForSnapshot(state controlplane.RelayPolicyState) ([]string, bool) {
	relays := c.configuredSidecarRelays()
	policyRelays := state.ContextVMRelays
	if len(policyRelays) == 0 {
		policyRelays = state.BrowserRelays
	}
	relays = appendUniqueRelays(relays, policyRelays...)
	if len(relays) == 0 {
		return nil, false
	}
	return relays, true
}

// serviceRelaysForSnapshot is the service pool for the canonical service
// relays; with the sidecar mirroring external relays the pool is fixed.
func (c *relayTopologyCoordinator) serviceRelaysForSnapshot(state controlplane.RelayPolicyState) ([]string, bool) {
	if len(state.ServiceRelays) == 0 || (c.nostrConfig.Sidecar.Enabled && c.nostrConfig.Sidecar.MirrorExternal) {
		return nil, false
	}
	return serviceRelayPoolURLs(c.nostrConfig, state.ServiceRelays, c.loomRelays), true
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

// driftLocked names the control-plane lists whose static configuration
// differs from the canonical lists in force.
func (c *relayTopologyCoordinator) driftLocked() []string {
	if c.source != relayTopologySourceCanonical {
		return nil
	}
	static := staticControlPlaneRelays(c.nostrConfig)
	var drift []string
	for _, list := range []struct {
		name            string
		static, current []string
	}{
		{"service_relays", static.Service, c.effective.Service},
		{"contextvm_relays", static.ContextVM, c.effective.ContextVM},
		{"browser_relays", static.Browser, c.effective.Browser},
	} {
		if !sameRelaySet(list.static, list.current) {
			drift = append(drift, list.name)
		}
	}
	return drift
}

func sameRelaySet(a, b []string) bool {
	normalize := func(relays []string) []string {
		out := make([]string, 0, len(relays))
		for _, relay := range relays {
			out = append(out, relayguard.Normalize(relay))
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	return slices.Equal(normalize(a), normalize(b))
}

// Health reports the relay topology in force: its source, the effective
// lists, the service write set and quorum, drift between the static and
// canonical lists, and any refused canonical settings (both warn).
func (c *relayTopologyCoordinator) Health() HealthCheck {
	c.mu.Lock()
	defer c.mu.Unlock()
	required := c.nostrConfig.PublishQuorum
	switch {
	case required == config.PublishQuorumAllRelays:
		required = len(c.serviceWrite)
	case required <= 0:
		required = config.PublishQuorumDefault
	}
	required = min(required, len(c.serviceWrite))
	static := staticControlPlaneRelays(c.nostrConfig)
	check := HealthCheck{
		Name:   "relay_topology",
		Status: HealthStatusPass,
		Details: map[string]string{
			"source":                c.source,
			"caught_up":             strconv.FormatBool(c.caughtUp),
			"service_relays":        strings.Join(c.effective.Service, ","),
			"service_write_relays":  strings.Join(c.serviceWrite, ","),
			"contextvm_relays":      strings.Join(c.effective.ContextVM, ","),
			"browser_relays":        strings.Join(c.effective.Browser, ","),
			"controlled_relays":     strings.Join(c.nostrConfig.ControlledRelaySet(), ","),
			"publish_quorum":        strconv.Itoa(c.nostrConfig.PublishQuorum),
			"required_acceptances":  strconv.Itoa(required),
			"static_service_relays": strings.Join(static.Service, ","),
		},
	}
	switch c.source {
	case relayTopologySourceCanonical:
		check.Message = "canonical relay settings govern the relay topology"
	default:
		check.Message = "static nostr relay lists are in force until canonical relay settings are received"
	}
	if c.nostrConfig.ControlledRelaysUnset() {
		check.Status = HealthStatusWarn
		check.Message = "nostr.controlled_relays is unset: only the sidecar is a controlled relay, so control-plane events reach no other relay; list the relays Bahia operates"
	}
	if !c.caughtUp {
		if waited := c.now().Sub(c.startedAt); waited > relayTopologyCatchUpWarnAfter {
			check.Status = HealthStatusWarn
			check.Message = "canonical relay settings have not caught up after " + waited.Round(time.Second).String() + " (no EOSE from every relay); relay sets are not advertised and the " + c.source + " relay topology stays in force"
		}
	}
	if drift := c.driftLocked(); len(drift) > 0 {
		check.Status = HealthStatusWarn
		check.Details["drift"] = strings.Join(drift, ",")
		check.Message = "static nostr relay configuration differs from the canonical relay settings in force (" + strings.Join(drift, ", ") + "); update the static lists, which apply on the next start until the canonical settings are received"
	}
	if c.refused != "" {
		check.Status = HealthStatusWarn
		check.Details["refused"] = c.refused
		check.Message = "canonical relay settings refused, the " + c.source + " relay topology stays in force: " + c.refused
	}
	return check
}

func (c *relayTopologyCoordinator) logReconfigureResult(poolName string, result nostrAdapter.RelayPoolReconfigureResult) {
	if !result.Changed {
		c.logger.Debug("relay pool topology already converged", zap.String("pool", poolName), zap.Strings("relays", result.CurrentURLs))
		return
	}
	fields := []zap.Field{
		zap.String("pool", poolName),
		zap.Strings("previous_relays", result.PreviousURLs),
		zap.Strings("current_relays", result.CurrentURLs),
		zap.Strings("added_relays", result.AddedURLs),
		zap.Strings("removed_relays", result.RemovedURLs),
		zap.Int("active_subscriptions", result.ActiveSubscriptions),
		zap.Int("migrated_subscriptions", result.MigratedSubscriptions),
	}
	if len(result.MigrationErrors) > 0 {
		c.logger.Warn("relay pool topology changed with active subscription migration errors", append(fields, zap.Strings("migration_errors", result.MigrationErrors))...)
		return
	}
	c.logger.Info("relay pool topology reconfigured from canonical relay policy", fields...)
}

func relayPolicySnapshotHasRuntimeTopology(state controlplane.RelayPolicyState) bool {
	return len(state.BrowserRelays)+len(state.ContextVMRelays)+len(state.ServiceRelays) > 0
}

func cloneNostrRelayTopologyConfig(cfg config.NostrConfig) config.NostrConfig {
	cfg.Relays = cloneAppStrings(cfg.Relays)
	cfg.ServiceRelays = cloneAppStrings(cfg.ServiceRelays)
	cfg.BrowserRelays = cloneAppStrings(cfg.BrowserRelays)
	cfg.ContextVMRelays = cloneAppStrings(cfg.ContextVMRelays)
	cfg.NIP34Relays = cloneAppStrings(cfg.NIP34Relays)
	cfg.ControlledRelays = cloneAppStrings(cfg.ControlledRelays)
	cfg.AllowedPublicRelays = cloneAppStrings(cfg.AllowedPublicRelays)
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
