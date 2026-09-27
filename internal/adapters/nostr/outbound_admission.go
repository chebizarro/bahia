package nostr

import (
	"errors"
	"fmt"
	"os"
	"strings"
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
	// ErrOutboundKillSwitch means the operator-controlled kill switch is active
	// or cannot be read safely. No relay I/O is attempted.
	ErrOutboundKillSwitch = errors.New("nostr outbound publication kill switch active")
	// ErrOutboundDuplicate means this exact signed event was already accepted by
	// a relay recently. Replaying it would consume relay capacity without
	// advancing state.
	ErrOutboundDuplicate = errors.New("nostr outbound publication duplicate suppressed")
)

type OutboundPurpose string

const (
	OutboundPurposePriority OutboundPurpose = "priority"
	OutboundPurposeState    OutboundPurpose = "state"
	OutboundPurposeGeneral  OutboundPurpose = "general"
)

type OutboundPurposeBudget struct {
	RatePerMinute int
	Burst         int
}

// OutboundAdmissionConfig bounds outbound relay traffic for one process. Rate
// is expressed in logical events, not relay fan-out attempts: one admitted
// event may be sent to every configured relay.
type OutboundAdmissionConfig struct {
	RatePerMinute  int
	Burst          int
	BreakerMin     time.Duration
	BreakerMax     time.Duration
	KillSwitchFile string
	DuplicateTTL   time.Duration
	DuplicateLimit int
	PurposeBudgets map[OutboundPurpose]OutboundPurposeBudget
}

// DefaultOutboundAdmissionConfig stays well below the shared relay's historic
// 120 events/minute bucket even when the Bahia server and one standalone agent
// are both active. Operators can lower this through an explicitly constructed
// controller; there is intentionally no unlimited default.
func DefaultOutboundAdmissionConfig() OutboundAdmissionConfig {
	return OutboundAdmissionConfig{
		RatePerMinute:  30,
		Burst:          10,
		BreakerMin:     2 * time.Second,
		BreakerMax:     time.Minute,
		KillSwitchFile: strings.TrimSpace(os.Getenv("BAHIA_NOSTR_OUTBOUND_KILL_SWITCH_FILE")),
		DuplicateTTL:   10 * time.Minute,
		DuplicateLimit: 4096,
		PurposeBudgets: map[OutboundPurpose]OutboundPurposeBudget{
			// Partitioning reserves capacity: projection/state churn cannot consume
			// the operator-result and tombstone lane.
			OutboundPurposePriority: {RatePerMinute: 10, Burst: 4},
			OutboundPurposeState:    {RatePerMinute: 10, Burst: 3},
			OutboundPurposeGeneral:  {RatePerMinute: 10, Burst: 3},
		},
	}
}

// OutboundAdmissionMetrics is a content-free snapshot suitable for health and
// telemetry. It never contains event bodies, tags, keys, or relay credentials.
type OutboundAdmissionMetrics struct {
	Attempted          uint64    `json:"attempted"`
	Admitted           uint64    `json:"admitted"`
	BudgetRejected     uint64    `json:"budget_rejected"`
	CircuitRejected    uint64    `json:"circuit_rejected"`
	RelayRateLimited   uint64    `json:"relay_rate_limited"`
	Duplicates         uint64    `json:"duplicates"`
	KillSwitchRejected uint64    `json:"kill_switch_rejected"`
	BreakerUntil       time.Time `json:"breaker_until,omitempty"`
}

type OutboundAdmissionState struct {
	Metrics          OutboundAdmissionMetrics
	CircuitOpen      bool
	KillSwitchActive bool
	KillSwitchError  string
}

type outboundBucket struct {
	ratePerSecond float64
	burst         float64
	tokens        float64
	lastRefill    time.Time
}

// OutboundAdmission is a process-wide token bucket and relay-feedback circuit
// breaker. Share one instance across every RelayPool in a process.
type OutboundAdmission struct {
	mu sync.Mutex

	buckets        map[OutboundPurpose]*outboundBucket
	breakerMin     time.Duration
	breakerMax     time.Duration
	breakerDelay   time.Duration
	breakerUntil   time.Time
	now            func() time.Time
	killSwitchFile string
	duplicateTTL   time.Duration
	duplicateLimit int
	accepted       map[string]time.Time
	acceptedOrder  []string

	metrics OutboundAdmissionMetrics
}

func NewOutboundAdmission(cfg OutboundAdmissionConfig) *OutboundAdmission {
	defaults := DefaultOutboundAdmissionConfig()
	if cfg.BreakerMin <= 0 {
		cfg.BreakerMin = DefaultOutboundAdmissionConfig().BreakerMin
	}
	if cfg.BreakerMax < cfg.BreakerMin {
		cfg.BreakerMax = cfg.BreakerMin
	}
	if cfg.DuplicateTTL <= 0 {
		cfg.DuplicateTTL = defaults.DuplicateTTL
	}
	if cfg.DuplicateLimit <= 0 {
		cfg.DuplicateLimit = defaults.DuplicateLimit
	}
	if len(cfg.PurposeBudgets) == 0 {
		cfg.PurposeBudgets = make(map[OutboundPurpose]OutboundPurposeBudget, len(defaults.PurposeBudgets))
		for purpose, budget := range defaults.PurposeBudgets {
			cfg.PurposeBudgets[purpose] = budget
		}
		// RatePerMinute/Burst are retained as a compact constructor for tests
		// and narrowly scoped processes with only general events.
		if cfg.RatePerMinute > 0 && cfg.Burst > 0 {
			cfg.PurposeBudgets[OutboundPurposeGeneral] = OutboundPurposeBudget{
				RatePerMinute: cfg.RatePerMinute,
				Burst:         cfg.Burst,
			}
		}
	}
	now := time.Now()
	buckets := make(map[OutboundPurpose]*outboundBucket, len(cfg.PurposeBudgets))
	for _, purpose := range []OutboundPurpose{OutboundPurposePriority, OutboundPurposeState, OutboundPurposeGeneral} {
		budget := cfg.PurposeBudgets[purpose]
		if budget.RatePerMinute <= 0 || budget.Burst <= 0 {
			budget = defaults.PurposeBudgets[purpose]
		}
		buckets[purpose] = &outboundBucket{
			ratePerSecond: float64(budget.RatePerMinute) / 60,
			burst:         float64(budget.Burst),
			tokens:        float64(budget.Burst),
			lastRefill:    now,
		}
	}
	return &OutboundAdmission{
		buckets:        buckets,
		breakerMin:     cfg.BreakerMin,
		breakerMax:     cfg.BreakerMax,
		now:            time.Now,
		killSwitchFile: strings.TrimSpace(cfg.KillSwitchFile),
		duplicateTTL:   cfg.DuplicateTTL,
		duplicateLimit: cfg.DuplicateLimit,
		accepted:       make(map[string]time.Time),
	}
}

func (a *OutboundAdmission) admit(ev gonostr.Event) error {
	if a == nil {
		return errors.New("nostr outbound admission is not configured")
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	now := a.now()
	a.metrics.Attempted++
	if active, err := outboundKillSwitchActive(a.killSwitchFile); err != nil || active {
		a.metrics.KillSwitchRejected++
		if err != nil {
			return fmt.Errorf("%w: %v", ErrOutboundKillSwitch, err)
		}
		return ErrOutboundKillSwitch
	}
	if now.Before(a.breakerUntil) {
		a.metrics.CircuitRejected++
		a.metrics.BreakerUntil = a.breakerUntil
		return fmt.Errorf("%w until %s", ErrOutboundCircuitOpen, a.breakerUntil.UTC().Format(time.RFC3339Nano))
	}
	a.pruneAcceptedLocked(now)
	id := ev.ID.Hex()
	if id != strings.Repeat("0", 64) {
		if acceptedAt, ok := a.accepted[id]; ok && now.Sub(acceptedAt) <= a.duplicateTTL {
			a.metrics.Duplicates++
			return ErrOutboundDuplicate
		}
	}

	bucket := a.buckets[outboundPurposeForEvent(ev)]
	elapsed := now.Sub(bucket.lastRefill).Seconds()
	if elapsed > 0 {
		bucket.tokens += elapsed * bucket.ratePerSecond
		if bucket.tokens > bucket.burst {
			bucket.tokens = bucket.burst
		}
		bucket.lastRefill = now
	}
	if bucket.tokens < 1 {
		a.metrics.BudgetRejected++
		return ErrOutboundBudgetExceeded
	}
	bucket.tokens--
	a.metrics.Admitted++
	return nil
}

func (a *OutboundAdmission) observe(ev gonostr.Event, results []PublishResult) {
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
		id := ev.ID.Hex()
		if id != strings.Repeat("0", 64) {
			a.accepted[id] = a.now()
			a.acceptedOrder = append(a.acceptedOrder, id)
			a.pruneAcceptedLocked(a.now())
		}
		a.breakerDelay = 0
		a.breakerUntil = time.Time{}
		a.metrics.BreakerUntil = time.Time{}
	}
}

func (a *OutboundAdmission) pruneAcceptedLocked(now time.Time) {
	for len(a.acceptedOrder) > 0 {
		id := a.acceptedOrder[0]
		at, ok := a.accepted[id]
		if len(a.accepted) <= a.duplicateLimit && ok && now.Sub(at) <= a.duplicateTTL {
			break
		}
		delete(a.accepted, id)
		a.acceptedOrder = a.acceptedOrder[1:]
	}
}

func outboundPurposeForEvent(ev gonostr.Event) OutboundPurpose {
	switch int(ev.Kind) {
	case 5, 1059:
		return OutboundPurposePriority
	}
	if ev.Kind >= 10000 {
		return OutboundPurposeState
	}
	return OutboundPurposeGeneral
}

func outboundKillSwitchActive(path string) (bool, error) {
	if path == "" {
		return false, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(string(data))) {
	case "1", "true", "stop", "stopped", "disable", "disabled":
		return true, nil
	default:
		return false, nil
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

func (a *OutboundAdmission) State() OutboundAdmissionState {
	if a == nil {
		return OutboundAdmissionState{KillSwitchActive: true, KillSwitchError: "outbound admission is not configured"}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	state := OutboundAdmissionState{
		Metrics:     a.metrics,
		CircuitOpen: a.now().Before(a.breakerUntil),
	}
	active, err := outboundKillSwitchActive(a.killSwitchFile)
	state.KillSwitchActive = active || err != nil
	if err != nil {
		state.KillSwitchError = err.Error()
	}
	return state
}
