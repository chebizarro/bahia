package app

// Tier identifies a closed subsystem availability level.
type Tier int

const (
	Tier0 Tier = 0
	Tier1 Tier = 1
	Tier2 Tier = 2
	Tier3 Tier = 3
)

// Mode identifies the requested application operating mode.
type Mode string

const (
	ModeFull      Mode = "full"
	ModeDegraded  Mode = "degraded"
	ModeEmergency Mode = "emergency"
)

// ModePolicy captures requested and active subsystem tiers.
type ModePolicy struct {
	RequestedMode Mode
	RequestedTier Tier
	ActiveTier    Tier

	// dependencyCap is the highest tier whose dependencies were actually
	// constructed (for example tier1 when Postgres is unavailable and every
	// tier2/tier3 repository is nil). Nothing may activate a tier above it.
	dependencyCap    Tier
	hasDependencyCap bool
}

// NewModePolicy derives the requested tier for the supplied mode.
func NewModePolicy(mode Mode) *ModePolicy {
	requestedTier := Tier3
	switch mode {
	case ModeDegraded:
		requestedTier = Tier2
	case ModeEmergency:
		requestedTier = Tier1
	}

	return &ModePolicy{
		RequestedMode: mode,
		RequestedTier: requestedTier,
		ActiveTier:    requestedTier,
	}
}

// AllowsTier reports whether a subsystem tier is available under the active tier.
func (p *ModePolicy) AllowsTier(t Tier) bool {
	return t <= p.ActiveTier
}

// RouteEnabled reports whether a route at the supplied tier is enabled.
func (p *ModePolicy) RouteEnabled(routeTier Tier) bool {
	return p.AllowsTier(routeTier)
}

// RunnerEnabled reports whether a background runner at the supplied tier is enabled.
func (p *ModePolicy) RunnerEnabled(runnerTier Tier) bool {
	return p.AllowsTier(runnerTier)
}

// RouteErrorBody returns the response body for a tier-gated route error.
func (p *ModePolicy) RouteErrorBody(requiredTier int) map[string]any {
	return map[string]any{
		"error":         "route unavailable in current mode",
		"mode":          string(p.RequestedMode),
		"active_tier":   int(p.ActiveTier),
		"required_tier": requiredTier,
	}
}

// CapTier records that dependencies above t were not constructed. The cap
// only ever tightens, and the active tier is lowered to it immediately.
func (p *ModePolicy) CapTier(t Tier) {
	if !p.hasDependencyCap || t < p.dependencyCap {
		p.dependencyCap = t
		p.hasDependencyCap = true
	}
	if p.ActiveTier > p.dependencyCap {
		p.ActiveTier = p.dependencyCap
	}
}

// MaxTier is the highest tier that may become active: the requested tier,
// limited by the dependency cap.
func (p *ModePolicy) MaxTier() Tier {
	if p.hasDependencyCap && p.dependencyCap < p.RequestedTier {
		return p.dependencyCap
	}
	return p.RequestedTier
}

// SetActiveTier records the tier established by bootstrap dependency checks,
// clamped to MaxTier so relay readiness can never un-gate routes or runners
// whose dependencies do not exist.
func (p *ModePolicy) SetActiveTier(t Tier) {
	if maxTier := p.MaxTier(); t > maxTier {
		t = maxTier
	}
	p.ActiveTier = t
}

// IsDegraded reports whether the active tier is lower than requested.
func (p *ModePolicy) IsDegraded() bool {
	return p.ActiveTier < p.RequestedTier
}
