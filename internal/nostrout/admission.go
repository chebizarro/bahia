// Package nostrout is Bahia's single outbound Nostr admission layer.
//
// Every EVENT publication that leaves a Bahia process — relay-pool fan-out,
// SoulFactory relay-bus writes, Signet management gift wraps, and NIP-46 signer
// RPCs — must be admitted by one process-wide Admission before any relay I/O.
// The controller is deliberately fail-closed: there is no unlimited mode, a nil
// controller rejects, an unreadable kill-switch file rejects, and relay
// rate-limit feedback opens a shared circuit breaker for every gateway.
//
// The package is a leaf: it depends only on the standard library and the Nostr
// library so that low-level adapters (Signet) and high-level ones (RelayPool,
// SoulFactory) can share the same controller without import cycles.
package nostrout

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
)

var (
	// ErrBudgetExceeded means a logical-event, lane, or per-relay budget is
	// exhausted. Callers must defer or retain the event; retrying immediately is
	// unsafe because it can amplify a broken reconciliation loop.
	ErrBudgetExceeded = errors.New("nostr outbound publication budget exhausted")
	// ErrCircuitOpen means a relay rate-limit response opened the shared
	// publication circuit breaker. No relay I/O is attempted while it is open.
	ErrCircuitOpen = errors.New("nostr outbound publication circuit breaker open")
	// ErrKillSwitch means the operator-controlled kill switch is active or cannot
	// be read safely. No relay I/O is attempted.
	ErrKillSwitch = errors.New("nostr outbound publication kill switch active")
	// ErrInFlight means the same signed event is already being sent to the same
	// relay. A concurrent copy would only consume relay capacity.
	ErrInFlight = errors.New("nostr outbound publication already in flight")
	// ErrCapacity means a bounded controller structure (active publications or
	// the relay-identity registry) is full. It is an explicit overflow, never a
	// silent drop.
	ErrCapacity = errors.New("nostr outbound admission capacity exhausted")
	// ErrQueueFull means the bounded operation or signer waiter queue is full.
	ErrQueueFull = errors.New("nostr outbound admission queue full")
	// ErrQueueTimeout means a bounded wait for admission expired before
	// capacity became available. Nothing was sent.
	ErrQueueTimeout = errors.New("nostr outbound admission wait expired")
	// ErrOperation reports invalid use of a bulk operation (declared size
	// exceeded, concurrent publications, or use after close).
	ErrOperation = errors.New("nostr outbound operation rejected")
	// ErrNotConfigured means a gateway was used without an admission controller.
	ErrNotConfigured = errors.New("nostr outbound admission is not configured")
)

// Purpose is an admission lane. Lanes partition the aggregate budget so that
// state-repair churn cannot consume capacity reserved for operator results and
// tombstones, and so that bulk security operations and signer RPCs are paced
// independently of both.
type Purpose string

const (
	// PurposePriority carries operator results, ContextVM traffic, gift wraps
	// and tombstones.
	PurposePriority Purpose = "priority"
	// PurposeState carries replaceable/addressable state projections.
	PurposeState Purpose = "state"
	// PurposeGeneral carries everything else.
	PurposeGeneral Purpose = "general"
	// PurposeBulk is used only by explicit multi-event operations (Concord
	// rotations and invites). Its events are paced rather than rejected.
	PurposeBulk Purpose = "bulk"
	// PurposeSigner carries NIP-46 remote-signer RPC events.
	PurposeSigner Purpose = "signer"
)

var allPurposes = []Purpose{PurposePriority, PurposeState, PurposeGeneral, PurposeBulk, PurposeSigner}

// PurposeBudget is a token-bucket budget expressed in events per minute.
type PurposeBudget struct {
	RatePerMinute int
	Burst         int
}

// Config bounds outbound relay traffic for one process. Logical budgets count
// events, not relay fan-out; RelayWire bounds actual EVENT frames per relay,
// including NIP-42 AUTH retries.
type Config struct {
	// Aggregate bounds all logical events across every lane. When unset it is
	// the sum of the lanes.
	Aggregate PurposeBudget
	// PurposeBudgets bounds each lane. Missing or non-positive lanes use the
	// defaults; a lane is clamped to Aggregate.
	PurposeBudgets map[Purpose]PurposeBudget
	// RelayWire bounds non-priority EVENT frames sent to any single relay.
	RelayWire PurposeBudget
	// RelayWirePriority is each relay's reserved wire allocation for priority
	// frames, so AUTH retries and non-priority churn cannot starve operator
	// results and tombstones at the relay.
	RelayWirePriority PurposeBudget

	// RatePerMinute/Burst are a compact constructor for narrowly scoped tests
	// and processes: when set they override the general lane.
	RatePerMinute int
	Burst         int

	BreakerMin time.Duration
	BreakerMax time.Duration

	KillSwitchFile string

	// DuplicateTTL/DuplicateLimit bound the per-destination receipt cache.
	DuplicateTTL   time.Duration
	DuplicateLimit int

	MaxActivePublications int
	MaxRelayIdentities    int
	MaxQueuedOperations   int
	MaxOperationEvents    int
	MaxQueueWait          time.Duration
	MaxOpaqueWaiters      int
}

const (
	defaultMaxActivePublications = 64
	defaultMaxRelayIdentities    = 128
	defaultMaxQueuedOperations   = 8
	defaultMaxOperationEvents    = 2048
	defaultMaxQueueWait          = 30 * time.Second
	defaultMaxOpaqueWaiters      = 32
	operationDeadlineSlack       = time.Minute
)

// DefaultConfig stays below the shared relay's historic 120 events/minute
// bucket even when the Bahia server and one standalone Bahia-derived agent are
// both active against the same relay (2 × (40 + 15) wire events/minute). There
// is intentionally no unlimited default.
func DefaultConfig() Config {
	return Config{
		Aggregate: PurposeBudget{RatePerMinute: 45, Burst: 15},
		PurposeBudgets: map[Purpose]PurposeBudget{
			PurposePriority: {RatePerMinute: 10, Burst: 4},
			PurposeState:    {RatePerMinute: 10, Burst: 3},
			PurposeGeneral:  {RatePerMinute: 10, Burst: 3},
			PurposeBulk:     {RatePerMinute: 5, Burst: 1},
			PurposeSigner:   {RatePerMinute: 10, Burst: 4},
		},
		RelayWire:             PurposeBudget{RatePerMinute: 40, Burst: 13},
		RelayWirePriority:     PurposeBudget{RatePerMinute: 15, Burst: 5},
		BreakerMin:            2 * time.Second,
		BreakerMax:            time.Minute,
		KillSwitchFile:        strings.TrimSpace(os.Getenv(KillSwitchEnv)),
		DuplicateTTL:          10 * time.Minute,
		DuplicateLimit:        4096,
		MaxActivePublications: defaultMaxActivePublications,
		MaxRelayIdentities:    defaultMaxRelayIdentities,
		MaxQueuedOperations:   defaultMaxQueuedOperations,
		MaxOperationEvents:    defaultMaxOperationEvents,
		MaxQueueWait:          defaultMaxQueueWait,
		MaxOpaqueWaiters:      defaultMaxOpaqueWaiters,
	}
}

// Metrics is a content-free snapshot suitable for health and telemetry. It
// never contains event bodies, tags, keys, or relay credentials.
type Metrics struct {
	Attempted          uint64    `json:"attempted"`
	Admitted           uint64    `json:"admitted"`
	BudgetRejected     uint64    `json:"budget_rejected"`
	CircuitRejected    uint64    `json:"circuit_rejected"`
	RelayRateLimited   uint64    `json:"relay_rate_limited"`
	Duplicates         uint64    `json:"duplicates"`
	KillSwitchRejected uint64    `json:"kill_switch_rejected"`
	InFlightRejected   uint64    `json:"in_flight_rejected"`
	CapacityRejected   uint64    `json:"capacity_rejected"`
	QueueRejected      uint64    `json:"queue_rejected"`
	WireAttempts       uint64    `json:"wire_attempts"`
	WireRejected       uint64    `json:"wire_rejected"`
	AuthAdmitted       uint64    `json:"auth_admitted"`
	OpaqueAdmitted     uint64    `json:"opaque_admitted"`
	OperationsStarted  uint64    `json:"operations_started"`
	OperationsQueued   int       `json:"operations_queued"`
	OperationActive    bool      `json:"operation_active"`
	ActivePublications int       `json:"active_publications"`
	BreakerUntil       time.Time `json:"breaker_until,omitempty"`
}

// State is Metrics plus derived gate status for readiness checks.
type State struct {
	Metrics          Metrics
	CircuitOpen      bool
	KillSwitchActive bool
	KillSwitchError  string
}

// clock is injectable so every budget, breaker, and queue test is
// deterministic without sleeps.
type clock interface {
	Now() time.Time
	NewTimer(time.Duration) (<-chan time.Time, func() bool)
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) NewTimer(d time.Duration) (<-chan time.Time, func() bool) {
	timer := time.NewTimer(d)
	return timer.C, timer.Stop
}

type bucket struct {
	ratePerSecond float64
	burst         float64
	tokens        float64
	lastRefill    time.Time
}

func newBucket(budget PurposeBudget, now time.Time) *bucket {
	return &bucket{
		ratePerSecond: float64(budget.RatePerMinute) / 60,
		burst:         float64(budget.Burst),
		tokens:        float64(budget.Burst),
		lastRefill:    now,
	}
}

func (b *bucket) refill(now time.Time) {
	elapsed := now.Sub(b.lastRefill).Seconds()
	if elapsed <= 0 {
		return
	}
	b.tokens = math.Min(b.burst, b.tokens+elapsed*b.ratePerSecond)
	b.lastRefill = now
}

// wait returns zero when a token is available, otherwise the time until one is.
func (b *bucket) wait(now time.Time) time.Duration {
	b.refill(now)
	if b.tokens >= 1 {
		return 0
	}
	seconds := (1 - b.tokens) / b.ratePerSecond
	return time.Duration(math.Ceil(seconds * float64(time.Second)))
}

func (b *bucket) full(now time.Time) bool {
	b.refill(now)
	return b.tokens >= b.burst
}

type relayBucket struct {
	priority *bucket
	other    *bucket
	refs     int
}

func (rb *relayBucket) forPurpose(purpose Purpose) *bucket {
	if purpose == PurposePriority {
		return rb.priority
	}
	return rb.other
}

func (rb *relayBucket) full(now time.Time) bool {
	return rb.priority.full(now) && rb.other.full(now)
}

type receiptKey struct {
	eventID string
	relay   string
}

type receipt struct {
	key        receiptKey
	acceptedAt time.Time
}

// Admission is the process-wide token-bucket, circuit-breaker, and duplicate
// suppression controller. Share one instance across every gateway in a
// process; use Default() unless a test needs isolation.
type Admission struct {
	mu sync.Mutex

	clock  clock
	jitter func(time.Duration) time.Duration

	aggregate    *bucket
	lanes        map[Purpose]*bucket
	wire         PurposeBudget
	wirePriority PurposeBudget
	relays       map[string]*relayBucket

	breakerMin        time.Duration
	breakerMax        time.Duration
	breakerDelay      time.Duration
	breakerUntil      time.Time
	breakerGeneration uint64

	killSwitchFile string

	duplicateTTL   time.Duration
	duplicateLimit int
	receipts       map[receiptKey]*list.Element
	receiptOrder   *list.List
	inflight       map[receiptKey]struct{}

	maxActive          int
	active             int
	maxRelayIdentities int

	maxQueuedOperations int
	maxOperationEvents  int
	maxQueueWait        time.Duration
	bulkRatePerSecond   float64
	operation           *Operation
	operationQueue      []*operationWaiter

	maxOpaqueWaiters int
	opaqueWaiters    int

	metrics Metrics
}

var (
	defaultOnce      sync.Once
	defaultAdmission *Admission
)

// KillSwitchEnv is the environment variable DefaultConfig reads for the
// emergency kill-switch file path. Processes that configure the controller
// explicitly (the Bahia server through internal/config) map the same
// variable onto Config.KillSwitchFile.
const KillSwitchEnv = "BAHIA_NOSTR_OUTBOUND_KILL_SWITCH_FILE"

// Default returns the process-wide controller. Every gateway constructed
// without an explicit controller shares it, so bounded defaults are genuinely
// process-wide rather than per-pool.
func Default() *Admission {
	defaultOnce.Do(func() {
		defaultAdmission = New(DefaultConfig())
	})
	return defaultAdmission
}

// InitDefault initializes the process-wide controller from cfg exactly once
// and returns it; cfg fields left zero keep DefaultConfig's bounded values.
// A process entry point calls it before constructing any gateway. When the
// default was already initialized — by an earlier InitDefault or by a
// Default() call — the existing controller is returned unchanged, so the
// first initialization wins and later ones are silent no-ops.
func InitDefault(cfg Config) *Admission {
	defaultOnce.Do(func() {
		defaultAdmission = New(cfg)
	})
	return defaultAdmission
}

// Or returns admission when non-nil and the process default otherwise. A nil
// injection never disables admission.
func Or(admission *Admission) *Admission {
	if admission != nil {
		return admission
	}
	return Default()
}

// New constructs an isolated controller. Production code should use Default;
// isolated controllers exist for tests and for explicitly bounded tools.
func New(cfg Config) *Admission {
	return newWithClock(cfg, realClock{})
}

func newWithClock(cfg Config, clk clock) *Admission {
	cfg = normalizeConfig(cfg)
	now := clk.Now()
	lanes := make(map[Purpose]*bucket, len(allPurposes))
	for _, purpose := range allPurposes {
		lanes[purpose] = newBucket(cfg.PurposeBudgets[purpose], now)
	}
	return &Admission{
		clock:               clk,
		jitter:              defaultJitter,
		aggregate:           newBucket(cfg.Aggregate, now),
		lanes:               lanes,
		wire:                cfg.RelayWire,
		wirePriority:        cfg.RelayWirePriority,
		relays:              make(map[string]*relayBucket),
		breakerMin:          cfg.BreakerMin,
		breakerMax:          cfg.BreakerMax,
		killSwitchFile:      strings.TrimSpace(cfg.KillSwitchFile),
		duplicateTTL:        cfg.DuplicateTTL,
		duplicateLimit:      cfg.DuplicateLimit,
		receipts:            make(map[receiptKey]*list.Element),
		receiptOrder:        list.New(),
		inflight:            make(map[receiptKey]struct{}),
		maxActive:           cfg.MaxActivePublications,
		maxRelayIdentities:  cfg.MaxRelayIdentities,
		maxQueuedOperations: cfg.MaxQueuedOperations,
		maxOperationEvents:  cfg.MaxOperationEvents,
		maxQueueWait:        cfg.MaxQueueWait,
		bulkRatePerSecond:   float64(cfg.PurposeBudgets[PurposeBulk].RatePerMinute) / 60,
		maxOpaqueWaiters:    cfg.MaxOpaqueWaiters,
	}
}

func normalizeConfig(cfg Config) Config {
	defaults := DefaultConfig()
	budgets := make(map[Purpose]PurposeBudget, len(allPurposes))
	for _, purpose := range allPurposes {
		budget := cfg.PurposeBudgets[purpose]
		if budget.RatePerMinute <= 0 || budget.Burst <= 0 {
			budget = defaults.PurposeBudgets[purpose]
		}
		budgets[purpose] = budget
	}
	if cfg.RatePerMinute > 0 && cfg.Burst > 0 {
		budgets[PurposeGeneral] = PurposeBudget{RatePerMinute: cfg.RatePerMinute, Burst: cfg.Burst}
	}
	if cfg.Aggregate.RatePerMinute <= 0 || cfg.Aggregate.Burst <= 0 {
		// An unset aggregate is the sum of the lanes, so it never admits more
		// than the lanes jointly allow.
		cfg.Aggregate = PurposeBudget{}
		for _, budget := range budgets {
			cfg.Aggregate.RatePerMinute += budget.RatePerMinute
			cfg.Aggregate.Burst += budget.Burst
		}
	}
	for purpose, budget := range budgets {
		budget.RatePerMinute = min(budget.RatePerMinute, cfg.Aggregate.RatePerMinute)
		budget.Burst = min(budget.Burst, cfg.Aggregate.Burst)
		budgets[purpose] = budget
	}
	cfg.PurposeBudgets = budgets
	if cfg.RelayWire.RatePerMinute <= 0 || cfg.RelayWire.Burst <= 0 {
		cfg.RelayWire = defaults.RelayWire
	}
	if cfg.RelayWirePriority.RatePerMinute <= 0 || cfg.RelayWirePriority.Burst <= 0 {
		cfg.RelayWirePriority = defaults.RelayWirePriority
	}
	if cfg.BreakerMin <= 0 {
		cfg.BreakerMin = defaults.BreakerMin
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
	if cfg.MaxActivePublications <= 0 {
		cfg.MaxActivePublications = defaultMaxActivePublications
	}
	if cfg.MaxRelayIdentities <= 0 {
		cfg.MaxRelayIdentities = defaultMaxRelayIdentities
	}
	if cfg.MaxQueuedOperations <= 0 {
		cfg.MaxQueuedOperations = defaultMaxQueuedOperations
	}
	if cfg.MaxOperationEvents <= 0 || cfg.MaxOperationEvents > defaultMaxOperationEvents {
		cfg.MaxOperationEvents = defaultMaxOperationEvents
	}
	if cfg.MaxQueueWait <= 0 || cfg.MaxQueueWait > defaultMaxQueueWait {
		cfg.MaxQueueWait = defaultMaxQueueWait
	}
	if cfg.MaxOpaqueWaiters <= 0 {
		cfg.MaxOpaqueWaiters = defaultMaxOpaqueWaiters
	}
	return cfg
}

// defaultJitter adds up to 20% so independently restarted processes do not
// retry against a recovering relay in lockstep.
func defaultJitter(delay time.Duration) time.Duration {
	if delay <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(delay)/5 + 1))
}

// PurposeForEvent classifies an event into its admission lane.
func PurposeForEvent(ev nostr.Event) Purpose {
	switch int(ev.Kind) {
	case 5, 1059, 25910:
		// Tombstones, gift-wrapped operator results, and ContextVM messages.
		return PurposePriority
	case 24133:
		return PurposeSigner
	}
	if ev.Kind >= 10000 && ev.Kind < 20000 || ev.Kind >= 30000 && ev.Kind < 40000 {
		return PurposeState
	}
	return PurposeGeneral
}

// NormalizeRelayURL returns the identity used for per-relay budgets and
// receipts: scheme and host are lower-cased and a bare root path is dropped.
// Non-root paths and queries are preserved because they can name distinct
// relays behind one host.
func NormalizeRelayURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return trimmed
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	if parsed.Path == "/" && parsed.RawQuery == "" {
		parsed.Path = ""
	}
	return parsed.String()
}

func normalizeRelayList(relays []string) []string {
	normalized := make([]string, 0, len(relays))
	seen := make(map[string]struct{}, len(relays))
	for _, relay := range relays {
		relay = NormalizeRelayURL(relay)
		if relay == "" {
			continue
		}
		if _, ok := seen[relay]; ok {
			continue
		}
		seen[relay] = struct{}{}
		normalized = append(normalized, relay)
	}
	return normalized
}

func eventKeyID(ev nostr.Event) string {
	if ev.ID == nostr.ZeroID {
		return ""
	}
	return ev.ID.Hex()
}

func killSwitchActive(path string) (bool, error) {
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

// killSwitchLocked rejects when the operator kill switch is active or its
// configured file cannot be read safely.
func (a *Admission) killSwitchLocked() error {
	active, err := killSwitchActive(a.killSwitchFile)
	if err != nil {
		a.metrics.KillSwitchRejected++
		return fmt.Errorf("%w: %v", ErrKillSwitch, err)
	}
	if active {
		a.metrics.KillSwitchRejected++
		return ErrKillSwitch
	}
	return nil
}

func (a *Admission) breakerWaitLocked(now time.Time) time.Duration {
	if now.Before(a.breakerUntil) {
		return a.breakerUntil.Sub(now)
	}
	return 0
}

func (a *Admission) circuitErrorLocked() error {
	a.metrics.CircuitRejected++
	a.metrics.BreakerUntil = a.breakerUntil
	return fmt.Errorf("%w until %s", ErrCircuitOpen, a.breakerUntil.UTC().Format(time.RFC3339Nano))
}

func (a *Admission) openBreakerLocked(now time.Time) {
	a.metrics.RelayRateLimited++
	a.breakerGeneration++
	if a.breakerDelay < a.breakerMin {
		a.breakerDelay = a.breakerMin
	} else {
		a.breakerDelay = min(2*a.breakerDelay, a.breakerMax)
	}
	until := now.Add(a.breakerDelay + a.jitter(a.breakerDelay))
	if until.After(a.breakerUntil) {
		a.breakerUntil = until
	}
	a.metrics.BreakerUntil = a.breakerUntil
}

// closeBreakerLocked resets backoff only for a success admitted under the
// current breaker generation after its cooldown: a late success from before a
// newer rate-limit response cannot clear that newer circuit.
func (a *Admission) closeBreakerLocked(now time.Time, generation uint64) {
	if generation != a.breakerGeneration || now.Before(a.breakerUntil) {
		return
	}
	a.breakerDelay = 0
	a.metrics.BreakerUntil = time.Time{}
}

// laneWaitLocked consumes aggregate+lane tokens and returns zero when both are
// available, otherwise it returns the wait until both could be.
func (a *Admission) laneWaitLocked(now time.Time, purpose Purpose) time.Duration {
	lane := a.lanes[purpose]
	wait := max(a.aggregate.wait(now), lane.wait(now))
	if wait > 0 {
		return wait
	}
	a.aggregate.tokens--
	lane.tokens--
	return 0
}

func (a *Admission) relayBucketLocked(now time.Time, relay string) (*relayBucket, error) {
	if rb, ok := a.relays[relay]; ok {
		return rb, nil
	}
	if len(a.relays) >= a.maxRelayIdentities {
		// Only idle identities whose buckets fully refilled may be evicted, so
		// recreating one can never restore burst capacity early.
		for identity, rb := range a.relays {
			if rb.refs == 0 && rb.full(now) {
				delete(a.relays, identity)
				break
			}
		}
		if len(a.relays) >= a.maxRelayIdentities {
			a.metrics.CapacityRejected++
			return nil, fmt.Errorf("%w: relay identity registry full", ErrCapacity)
		}
	}
	rb := &relayBucket{priority: newBucket(a.wirePriority, now), other: newBucket(a.wire, now)}
	a.relays[relay] = rb
	return rb, nil
}

func (a *Admission) pruneReceiptsLocked(now time.Time) {
	for a.receiptOrder.Len() > 0 {
		front := a.receiptOrder.Front()
		rec := front.Value.(receipt)
		if a.receiptOrder.Len() <= a.duplicateLimit && now.Sub(rec.acceptedAt) <= a.duplicateTTL {
			return
		}
		a.receiptOrder.Remove(front)
		delete(a.receipts, rec.key)
	}
}

func (a *Admission) storeReceiptLocked(now time.Time, key receiptKey) {
	if element, ok := a.receipts[key]; ok {
		element.Value = receipt{key: key, acceptedAt: now}
		a.receiptOrder.MoveToBack(element)
	} else {
		a.receipts[key] = a.receiptOrder.PushBack(receipt{key: key, acceptedAt: now})
	}
	a.pruneReceiptsLocked(now)
}

func (a *Admission) hasReceiptLocked(now time.Time, key receiptKey) bool {
	element, ok := a.receipts[key]
	if !ok {
		return false
	}
	return now.Sub(element.Value.(receipt).acceptedAt) <= a.duplicateTTL
}

// waitLocked repeatedly calls try until it returns zero wait or an error. It is
// entered and returns with a.mu held; the lock is released while waiting on a
// cancellable, injectable timer — never by polling.
func (a *Admission) waitLocked(ctx context.Context, deadline time.Time, try func(time.Time) (time.Duration, error)) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		now := a.clock.Now()
		// The deadline is checked before try so an expired waiter can never
		// consume capacity, even if tokens happen to be available.
		if !deadline.IsZero() && !now.Before(deadline) {
			return ErrQueueTimeout
		}
		wait, err := try(now)
		if err != nil || wait <= 0 {
			return err
		}
		if !deadline.IsZero() {
			wait = min(wait, deadline.Sub(now))
		}
		fired, stop := a.clock.NewTimer(wait)
		a.mu.Unlock()
		select {
		case <-ctx.Done():
			stop()
			a.mu.Lock()
			return ctx.Err()
		case <-fired:
		}
		a.mu.Lock()
	}
}

// AdmitAuth charges one NIP-42 AUTH frame to relayURL immediately before the
// frame is written. AUTH is not an EVENT publication, but it is a relay frame
// a hostile or flapping relay can demand without bound, so it needs a permit:
// one priority-lane token and one of the relay's reserved priority wire
// tokens. The priority share is the right home — AUTH unlocks delivery of
// exactly the traffic that share reserves (inbox relays serve gift-wrapped
// operator results only to their authenticated recipient), and its bounded
// burst caps AUTH storms: when the share is spent the attempt fails closed
// and the caller surfaces a retryable authentication failure instead of
// writing the frame. The kill switch and an open breaker refuse AUTH before
// any I/O like every other frame.
func (a *Admission) AdmitAuth(ctx context.Context, relayURL string) error {
	if a == nil {
		return ErrNotConfigured
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	relay := NormalizeRelayURL(relayURL)
	if relay == "" {
		return ErrNoDestinations
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.killSwitchLocked(); err != nil {
		return err
	}
	now := a.clock.Now()
	if a.breakerWaitLocked(now) > 0 {
		return a.circuitErrorLocked()
	}
	rb, err := a.relayBucketLocked(now, relay)
	if err != nil {
		return err
	}
	// Fail fast, and spend nothing on a refusal: check both buckets, then
	// consume both under the same lock hold.
	wire := rb.forPurpose(PurposePriority)
	laneWait := max(a.aggregate.wait(now), a.lanes[PurposePriority].wait(now))
	wireWait := wire.wait(now)
	if laneWait > 0 || wireWait > 0 {
		if wireWait > 0 {
			a.metrics.WireRejected++
			return fmt.Errorf("%w: relay %s AUTH wire budget", ErrBudgetExceeded, relay)
		}
		a.metrics.BudgetRejected++
		return ErrBudgetExceeded
	}
	a.aggregate.tokens--
	a.lanes[PurposePriority].tokens--
	wire.tokens--
	a.metrics.WireAttempts++
	a.metrics.AuthAdmitted++
	return nil
}

// ReportRateLimited opens or extends the shared circuit breaker for relay
// rate-limit feedback that arrives outside a publication result: a
// subscription CLOSED or a NOTICE frame whose message carries the
// rate-limited: prefix. It is safe to call concurrently and after Close of
// any publication.
func (a *Admission) ReportRateLimited() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.openBreakerLocked(a.clock.Now())
}

// Metrics returns content-free counters.
func (a *Admission) Metrics() Metrics {
	if a == nil {
		return Metrics{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.metricsLocked()
}

func (a *Admission) metricsLocked() Metrics {
	metrics := a.metrics
	metrics.OperationsQueued = len(a.operationQueue)
	metrics.OperationActive = a.operation != nil
	metrics.ActivePublications = a.active
	return metrics
}

// State returns metrics plus gate status. A nil controller reports an active
// kill switch because it would reject every publication.
func (a *Admission) State() State {
	if a == nil {
		return State{KillSwitchActive: true, KillSwitchError: ErrNotConfigured.Error()}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	state := State{
		Metrics:     a.metricsLocked(),
		CircuitOpen: a.clock.Now().Before(a.breakerUntil),
	}
	active, err := killSwitchActive(a.killSwitchFile)
	state.KillSwitchActive = active || err != nil
	if err != nil {
		state.KillSwitchError = err.Error()
	}
	return state
}
