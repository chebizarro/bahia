package nostr

import (
	"errors"
	"fmt"
	"sync"
	"time"

	gonostr "fiatjaf.com/nostr"
)

var (
	// ErrOutboundBudgetExceeded means the process-wide publication budget is
	// exhausted. Callers must defer or retain the event; retrying immediately is
	// explicitly unsafe because it can amplify a broken reconciliation loop.
	ErrOutboundBudgetExceeded = errors.New("nostr outbound publication budget exhausted")
	// ErrOutboundCircuitOpen means a relay rate-limit response opened the shared
	// publication circuit breaker. No relay I/O is attempted while it is open.
	ErrOutboundCircuitOpen = errors.New("nostr outbound publication circuit breaker open")
)

// OutboundAdmissionConfig bounds outbound relay traffic for one process. Rate
// is expressed in logical events, not relay fan-out attempts: one admitted
// event may be sent to every configured relay.
type OutboundAdmissionConfig struct {
	RatePerMinute int
	Burst         int
	BreakerMin    time.Duration
	BreakerMax    time.Duration
}

// DefaultOutboundAdmissionConfig stays well below the shared relay's historic
// 120 events/minute bucket even when the Bahia server and one standalone agent
// are both active. Operators can lower this through an explicitly constructed
// controller; there is intentionally no unlimited default.
func DefaultOutboundAdmissionConfig() OutboundAdmissionConfig {
	return OutboundAdmissionConfig{
		RatePerMinute: 30,
		Burst:         10,
		BreakerMin:    2 * time.Second,
		BreakerMax:    time.Minute,
	}
}

// OutboundAdmissionMetrics is a content-free snapshot suitable for health and
// telemetry. It never contains event bodies, tags, keys, or relay credentials.
type OutboundAdmissionMetrics struct {
	Attempted        uint64    `json:"attempted"`
	Admitted         uint64    `json:"admitted"`
	BudgetRejected   uint64    `json:"budget_rejected"`
	CircuitRejected  uint64    `json:"circuit_rejected"`
	RelayRateLimited uint64    `json:"relay_rate_limited"`
	BreakerUntil     time.Time `json:"breaker_until,omitempty"`
}

// OutboundAdmission is a process-wide token bucket and relay-feedback circuit
// breaker. Share one instance across every RelayPool in a process.
type OutboundAdmission struct {
	mu sync.Mutex

	ratePerSecond float64
	burst         float64
	tokens        float64
	lastRefill    time.Time
	breakerMin    time.Duration
	breakerMax    time.Duration
	breakerDelay  time.Duration
	breakerUntil  time.Time
	now           func() time.Time

	metrics OutboundAdmissionMetrics
}

func NewOutboundAdmission(cfg OutboundAdmissionConfig) *OutboundAdmission {
	if cfg.RatePerMinute <= 0 {
		cfg.RatePerMinute = DefaultOutboundAdmissionConfig().RatePerMinute
	}
	if cfg.Burst <= 0 {
		cfg.Burst = DefaultOutboundAdmissionConfig().Burst
	}
	if cfg.BreakerMin <= 0 {
		cfg.BreakerMin = DefaultOutboundAdmissionConfig().BreakerMin
	}
	if cfg.BreakerMax < cfg.BreakerMin {
		cfg.BreakerMax = cfg.BreakerMin
	}
	now := time.Now()
	return &OutboundAdmission{
		ratePerSecond: float64(cfg.RatePerMinute) / 60,
		burst:         float64(cfg.Burst),
		tokens:        float64(cfg.Burst),
		lastRefill:    now,
		breakerMin:    cfg.BreakerMin,
		breakerMax:    cfg.BreakerMax,
		now:           time.Now,
	}
}

func (a *OutboundAdmission) admit(_ gonostr.Event) error {
	if a == nil {
		return errors.New("nostr outbound admission is not configured")
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	now := a.now()
	a.metrics.Attempted++
	if now.Before(a.breakerUntil) {
		a.metrics.CircuitRejected++
		a.metrics.BreakerUntil = a.breakerUntil
		return fmt.Errorf("%w until %s", ErrOutboundCircuitOpen, a.breakerUntil.UTC().Format(time.RFC3339Nano))
	}

	elapsed := now.Sub(a.lastRefill).Seconds()
	if elapsed > 0 {
		a.tokens += elapsed * a.ratePerSecond
		if a.tokens > a.burst {
			a.tokens = a.burst
		}
		a.lastRefill = now
	}
	if a.tokens < 1 {
		a.metrics.BudgetRejected++
		return ErrOutboundBudgetExceeded
	}
	a.tokens--
	a.metrics.Admitted++
	return nil
}

func (a *OutboundAdmission) observe(results []PublishResult) {
	if a == nil {
		return
	}
	rateLimited := false
	accepted := false
	for _, result := range results {
		rateLimited = rateLimited || result.IsRateLimited()
		accepted = accepted || result.Accepted || IsDuplicateReason(result.Reason)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if rateLimited {
		a.metrics.RelayRateLimited++
		if a.breakerDelay < a.breakerMin {
			a.breakerDelay = a.breakerMin
		} else {
			a.breakerDelay *= 2
			if a.breakerDelay > a.breakerMax {
				a.breakerDelay = a.breakerMax
			}
		}
		a.breakerUntil = a.now().Add(a.breakerDelay)
		a.metrics.BreakerUntil = a.breakerUntil
		return
	}
	if accepted {
		a.breakerDelay = 0
		a.breakerUntil = time.Time{}
		a.metrics.BreakerUntil = time.Time{}
	}
}

func (a *OutboundAdmission) Metrics() OutboundAdmissionMetrics {
	if a == nil {
		return OutboundAdmissionMetrics{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.metrics
}
