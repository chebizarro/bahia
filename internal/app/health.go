package app

import (
	"fmt"
	"sync"
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
	Tier    int
}

type HealthSnapshot struct {
	Status        string
	Mode          string
	RequestedTier int
	ActiveTier    int
	Ready         bool
	Checks        []HealthCheck
	RunnerSummary []RunnerStatus
}

type registeredHealthCheck struct {
	name string
	tier int
	fn   func() HealthCheck
}

type RelayQuorumConfig struct {
	FullMinHealthy      int
	DegradedMinHealthy  int
	EmergencyMinHealthy int
}

type HealthProvider struct {
	modePolicy *ModePolicy
	background *BackgroundManager
	// Slots for future providers (relay pool, bootstrap state, etc.)
	mu                sync.RWMutex
	relayHealthFn     func() (connected, healthy int)
	bootstrapFn       func() (phase string, ready bool)
	relayQuorumConfig RelayQuorumConfig
	checks            []registeredHealthCheck
}

func NewHealthProvider(policy *ModePolicy, bg *BackgroundManager) *HealthProvider {
	if policy == nil {
		policy = NewModePolicy(ModeFull)
	}
	return &HealthProvider{modePolicy: policy, background: bg, relayQuorumConfig: DefaultRelayQuorumConfig()}
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

func (p *HealthProvider) RegisterCheck(name string, tier int, fn func() HealthCheck) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.checks = append(p.checks, registeredHealthCheck{name: name, tier: tier, fn: fn})
}

func (p *HealthProvider) Liveness() HealthSnapshot {
	snapshot := p.baseSnapshot()
	snapshot.Status = SnapshotStatusHealthy
	snapshot.Ready = true
	return snapshot
}

func (p *HealthProvider) Readiness() HealthSnapshot {
	snapshot := p.baseSnapshot()

	p.mu.RLock()
	relayHealthFn := p.relayHealthFn
	bootstrapFn := p.bootstrapFn
	relayQuorumConfig := p.relayQuorumConfig
	registeredChecks := append([]registeredHealthCheck(nil), p.checks...)
	p.mu.RUnlock()

	snapshot.Checks = append(snapshot.Checks, relayQuorumCheck(relayHealthFn, snapshot.ActiveTier, currentMode(p.modePolicy), relayQuorumConfig))
	snapshot.Checks = append(snapshot.Checks, bootstrapReadyCheck(bootstrapFn, snapshot.ActiveTier))
	snapshot.Checks = append(snapshot.Checks, p.backgroundRunnersCheck(snapshot.ActiveTier))
	for _, registered := range registeredChecks {
		if registered.fn == nil || registered.tier > snapshot.ActiveTier {
			continue
		}
		check := registered.fn()
		if check.Name == "" {
			check.Name = registered.name
		}
		if check.Tier == 0 && registered.tier != 0 {
			check.Tier = registered.tier
		}
		snapshot.Checks = append(snapshot.Checks, check)
	}

	snapshot.Ready = checksPass(snapshot.Checks)
	snapshot.Status = SnapshotStatusHealthy
	if !snapshot.Ready {
		snapshot.Status = SnapshotStatusUnhealthy
	} else if p.modePolicy.IsDegraded() {
		snapshot.Status = SnapshotStatusDegraded
	}
	return snapshot
}

func (p *HealthProvider) baseSnapshot() HealthSnapshot {
	policy := p.modePolicy
	if policy == nil {
		policy = NewModePolicy(ModeFull)
	}

	snapshot := HealthSnapshot{
		Status:        SnapshotStatusUnknown,
		Mode:          string(policy.RequestedMode),
		RequestedTier: int(policy.RequestedTier),
		ActiveTier:    int(policy.ActiveTier),
	}
	if p.background != nil {
		snapshot.RunnerSummary = p.background.RunnerStatuses()
	}
	return snapshot
}

func relayQuorumCheck(fn func() (connected, healthy int), tier int, mode Mode, config RelayQuorumConfig) HealthCheck {
	minRequired := minHealthyForMode(mode, config)
	check := HealthCheck{Name: "relay_quorum", Status: HealthStatusPass, Message: fmt.Sprintf("relay health provider not configured, min_required=%d", minRequired), Tier: tier}
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

func currentMode(policy *ModePolicy) Mode {
	if policy == nil {
		return ModeFull
	}
	switch policy.ActiveTier {
	case Tier1:
		return ModeEmergency
	case Tier2:
		return ModeDegraded
	default:
		return ModeFull
	}
}

func minHealthyForMode(mode Mode, config RelayQuorumConfig) int {
	config = normalizeRelayQuorumConfig(config)
	switch mode {
	case ModeEmergency:
		return config.EmergencyMinHealthy
	case ModeDegraded:
		return config.DegradedMinHealthy
	default:
		return config.FullMinHealthy
	}
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

func bootstrapReadyCheck(fn func() (phase string, ready bool), tier int) HealthCheck {
	check := HealthCheck{Name: "bootstrap_ready", Status: HealthStatusPass, Message: "bootstrap provider not configured", Tier: tier}
	if fn == nil {
		return check
	}

	phase, ready := fn()
	check.Message = fmt.Sprintf("phase=%s", phase)
	if ready {
		return check
	}
	check.Status = HealthStatusFail
	return check
}

func (p *HealthProvider) backgroundRunnersCheck(activeTier int) HealthCheck {
	check := HealthCheck{Name: "background_runners", Status: HealthStatusPass, Message: "required runners are running", Tier: activeTier}
	if p.background == nil {
		check.Message = "background manager not configured"
		return check
	}

	statuses := p.background.RunnerStatuses()
	missing := 0
	failed := 0
	for _, status := range statuses {
		if !status.Required || status.Tier > activeTier {
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

func checksPass(checks []HealthCheck) bool {
	for _, check := range checks {
		if check.Status != HealthStatusPass {
			return false
		}
	}
	return true
}
