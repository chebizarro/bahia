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

// SetActiveTier records the tier established by bootstrap dependency checks.
func (p *ModePolicy) SetActiveTier(t Tier) {
	p.ActiveTier = t
}

// IsDegraded reports whether the active tier is lower than requested.
func (p *ModePolicy) IsDegraded() bool {
	return p.ActiveTier < p.RequestedTier
}
