package app

import (
	"fmt"
	"strings"
	"sync"

	"github.com/openagentsinc/bahia/internal/controlplane"
)

const (
	HealthStatusPass    = "pass"
	HealthStatusFail    = "fail"
	HealthStatusWarn    = "warn"
	HealthStatusUnknown = "unknown"

	SnapshotStatusHealthy   = "healthy"
	SnapshotStatusDegraded  = "degraded"
	SnapshotStatusUnhealthy = "unhealthy"
	SnapshotStatusUnknown   = "unknown"
)

type HealthCheck struct {
	Name    string
	Status  string
	Message string
	Details map[string]string
}

// HealthSnapshot is the readiness/liveness state for endpoints. Tier fields
// are removed — readiness is gated on the ReadinessTracker (§6.2).
type HealthSnapshot struct {
	Status string
	Ready  bool
	Checks []HealthCheck
}

type registeredHealthCheck struct {
	name string
	fn   func() HealthCheck
}

type RelayQuorumConfig struct {
	FullMinHealthy      int
	DegradedMinHealthy  int
	EmergencyMinHealthy int
}

type HealthProvider struct {
	readiness          *controlplane.ReadinessTracker
	background         *BackgroundManager
	mu                 sync.RWMutex
	relayHealthFn      func() (connected, healthy int)
	bootstrapFn        func() (phase string, ready bool)
	bootstrapDetailsFn func() map[string]string
	relayQuorumConfig  RelayQuorumConfig
	checks             []registeredHealthCheck
}

func NewHealthProvider(readiness *controlplane.ReadinessTracker, bg *BackgroundManager) *HealthProvider {
	return &HealthProvider{readiness: readiness, background: bg, relayQuorumConfig: DefaultRelayQuorumConfig()}
}

func (p *HealthProvider) SetReadinessTracker(r *controlplane.ReadinessTracker) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.readiness = r
}

func DefaultRelayQuorumConfig() RelayQuorumConfig {
	return RelayQuorumConfig{FullMinHealthy: 2, DegradedMinHealthy: 1, EmergencyMinHealthy: 1}
}

func (p *HealthProvider) SetRelayQuorumConfig(config RelayQuorumConfig) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.relayQuorumConfig = normalizeRelayQuorumConfig(config)
}

func (p *HealthProvider) SetRelayHealthFunc(fn func() (connected, healthy int)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.relayHealthFn = fn
}

func (p *HealthProvider) SetBootstrapFunc(fn func() (phase string, ready bool)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bootstrapFn = fn
}

// SetBootstrapDetailsFunc supplies non-secret bootstrap diagnostics for the
// readiness response while preserving the existing readiness contract.
func (p *HealthProvider) SetBootstrapDetailsFunc(fn func() map[string]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bootstrapDetailsFn = fn
}

func (p *HealthProvider) RegisterCheck(name string, fn func() HealthCheck) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.checks = append(p.checks, registeredHealthCheck{name: name, fn: fn})
}

func (p *HealthProvider) Liveness() HealthSnapshot {
	return HealthSnapshot{Status: SnapshotStatusHealthy, Ready: true}
}

func (p *HealthProvider) Readiness() HealthSnapshot {
	snapshot := HealthSnapshot{Status: SnapshotStatusUnknown}

	p.mu.RLock()
	relayHealthFn := p.relayHealthFn
	bootstrapFn := p.bootstrapFn
	bootstrapDetailsFn := p.bootstrapDetailsFn
	relayQuorumConfig := p.relayQuorumConfig
	registeredChecks := append([]registeredHealthCheck(nil), p.checks...)
	p.mu.RUnlock()

	snapshot.Checks = append(snapshot.Checks, relayQuorumCheck(relayHealthFn, relayQuorumConfig))
	snapshot.Checks = append(snapshot.Checks, bootstrapReadyCheck(bootstrapFn, bootstrapDetailsFn))
	snapshot.Checks = append(snapshot.Checks, p.backgroundRunnersCheck())

	// Intent readiness check: the ReadinessTracker must report all filters
	// synced. This replaces the old tier-based readiness.
	if p.readiness != nil {
		progress := p.readiness.Progress()
		status := HealthStatusPass
		msg := "all filters synced"
		if !progress.Ready {
			status = HealthStatusFail
			msg = "filters syncing"
		}
		snapshot.Checks = append(snapshot.Checks, HealthCheck{
			Name: "intent_readiness", Status: status, Message: msg,
		})
	}

	for _, registered := range registeredChecks {
		if registered.fn == nil {
			continue
		}
		check := registered.fn()
		if check.Name == "" {
			check.Name = registered.name
		}
		snapshot.Checks = append(snapshot.Checks, check)
	}

	snapshot.Ready = checksPass(snapshot.Checks)
	snapshot.Status = SnapshotStatusHealthy
	if !snapshot.Ready {
		snapshot.Status = SnapshotStatusUnhealthy
	} else if checksWarn(snapshot.Checks) {
		snapshot.Status = SnapshotStatusDegraded
	}
	return snapshot
}

func relayQuorumCheck(fn func() (connected, healthy int), config RelayQuorumConfig) HealthCheck {
	minRequired := config.FullMinHealthy
	if minRequired <= 0 {
		minRequired = DefaultRelayQuorumConfig().FullMinHealthy
	}
	check := HealthCheck{Name: "relay_quorum", Status: HealthStatusPass, Message: fmt.Sprintf("relay health provider not configured, min_required=%d", minRequired)}
	if fn == nil {
		return check
	}
	connected, healthy := fn()
	check.Message = fmt.Sprintf("%d connected, %d healthy, min_required=%d", connected, healthy, minRequired)
	if healthy >= minRequired {
		return check
	}
	check.Status = HealthStatusFail
	return check
}

func bootstrapReadyCheck(fn func() (phase string, ready bool), detailsFn func() map[string]string) HealthCheck {
	check := HealthCheck{Name: "bootstrap_ready", Status: HealthStatusPass, Message: "bootstrap provider not configured"}
	if fn == nil {
		return check
	}
	phase, ready := fn()
	check.Message = fmt.Sprintf("phase=%s", phase)
	if detailsFn != nil {
		check.Details = detailsFn()
	}
	if ready {
		return check
	}
	check.Status = HealthStatusFail
	return check
}

func (p *HealthProvider) backgroundRunnersCheck() HealthCheck {
	check := HealthCheck{Name: "background_runners", Status: HealthStatusPass, Message: "required runners are running"}
	if p.background == nil {
		check.Message = "background manager not configured"
		return check
	}
	statuses := p.background.RunnerStatuses()
	missing := 0
	failed := 0
	for _, status := range statuses {
		if !status.Required {
			continue
		}
		if !status.Running {
			missing++
		}
		if status.LastError != nil {
			failed++
		}
	}
	if missing == 0 && failed == 0 {
		return check
	}
	check.Status = HealthStatusFail
	check.Message = fmt.Sprintf("%d required runners stopped, %d failed", missing, failed)
	return check
}

func normalizeRelayQuorumConfig(config RelayQuorumConfig) RelayQuorumConfig {
	defaults := DefaultRelayQuorumConfig()
	if config.FullMinHealthy <= 0 {
		config.FullMinHealthy = defaults.FullMinHealthy
	}
	if config.DegradedMinHealthy <= 0 {
		config.DegradedMinHealthy = defaults.DegradedMinHealthy
	}
	if config.EmergencyMinHealthy <= 0 {
		config.EmergencyMinHealthy = defaults.EmergencyMinHealthy
	}
	return config
}

func checksPass(checks []HealthCheck) bool {
	for _, check := range checks {
		if check.Status == HealthStatusFail || check.Status == HealthStatusUnknown {
			return false
		}
	}
	return true
}

func checksWarn(checks []HealthCheck) bool {
	for _, check := range checks {
		if check.Status == HealthStatusWarn {
			return true
		}
	}
	return false
}

// Rotation degrades org writes, not subscription catch-up or liveness. Keep it
// separate from ReadinessTracker's one-shot EOSE gate so operators can retry.
func registerOCKRotationHealthCheck(provider *HealthProvider, pendingRotations func() []string) {
	provider.RegisterCheck("ock_rotation", func() HealthCheck {
		pending := pendingRotations()
		if len(pending) == 0 {
			return HealthCheck{Name: "ock_rotation", Status: HealthStatusPass, Message: "no pending key rotations"}
		}
		messages := make([]string, 0, len(pending))
		for _, orgID := range pending {
			messages = append(messages, (&controlplane.OCKRotationPendingError{OrgID: orgID}).Error())
		}
		return HealthCheck{Name: "ock_rotation", Status: HealthStatusWarn,
			Message: strings.Join(messages, "; "), Details: map[string]string{"orgs": strings.Join(pending, ",")}}
	})
}
