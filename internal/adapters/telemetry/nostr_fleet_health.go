package telemetry

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
)

const maxFleetHealthEventEntities = 4000

// fleetDomainRoute is the bounded fleet-health domain for managed-route canary
// observables. It matches the CAS `domain` tag Bahia's route canary projector
// publishes, so route outages count separately from the runtime containers
// behind them.
const fleetDomainRoute = "route"

// nostrFleetHealthDomains is the single bounded vocabulary for the `domain`
// label of bahia_fleet_health_nostr_entities. Classification and metric
// rendering both read it, so a domain can never be classified without also
// being exported.
var nostrFleetHealthDomains = []string{"agent", "worker", "service", "deployment", "runtime", fleetDomainRoute, "relay", "control_plane"}

// fleetHealthClass says how a canonical observable contributes to the snapshot.
type fleetHealthClass int

const (
	// fleetHealthInvalid observables cannot be attributed and count as
	// projection errors.
	fleetHealthInvalid fleetHealthClass = iota
	// fleetHealthEntity observables define or update one subject entity.
	fleetHealthEntity
	// fleetHealthLineage observables are immutable history about an entity that
	// is already represented by its current-state observables; they neither
	// create nor overwrite an entity.
	fleetHealthLineage
)

var observableFleetHealthKinds = map[int]struct{}{
	kinds.NIP38Status: {}, kinds.AssistantTranscript: {},
	kinds.SoulFactoryRuntimeCapability: {}, kinds.CASControlState: {}, kinds.CASAudit: {},
}

type nostrFleetHealthEntity struct {
	domain     string
	status     string
	label      string
	eventAt    time.Time
	ingestedAt time.Time
}

type NostrFleetHealthSnapshot struct {
	SubscriptionActive  bool
	CaughtUp            bool
	LastEventAt         time.Time
	LastIngestedAt      time.Time
	RelayClosedTotal    uint64
	ProjectionErrors    uint64
	Entities            map[string]int
	HeartbeatLagSeconds map[string]float64
}

type nostrFleetHealthProjector struct {
	mu                 sync.RWMutex
	entities           map[string]nostrFleetHealthEntity
	subscriptionActive bool
	caughtUp           bool
	lastEventAt        time.Time
	lastIngestedAt     time.Time
	relayClosedTotal   uint64
	projectionErrors   uint64
	now                func() time.Time
}

func newNostrFleetHealthProjector(now func() time.Time) *nostrFleetHealthProjector {
	return &nostrFleetHealthProjector{entities: make(map[string]nostrFleetHealthEntity), now: now}
}

func (p *nostrFleetHealthProjector) observeEvent(_ context.Context, ev *gonostr.Event) {
	if ev == nil {
		return
	}
	kind := int(ev.Kind)
	if _, ok := observableFleetHealthKinds[kind]; !ok {
		return
	}
	domain, status, key, class := classifyFleetHealthEvent(ev)
	if class == fleetHealthLineage {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if class != fleetHealthEntity {
		p.projectionErrors++
		return
	}
	if current, exists := p.entities[key]; exists && !ev.CreatedAt.Time().After(current.eventAt) {
		return
	}
	if len(p.entities) >= maxFleetHealthEventEntities {
		if _, exists := p.entities[key]; !exists {
			p.projectionErrors++
			return
		}
	}
	ingestedAt := p.now().UTC()
	p.entities[key] = nostrFleetHealthEntity{domain: domain, status: status, label: ev.PubKey.Hex(), eventAt: ev.CreatedAt.Time().UTC(), ingestedAt: ingestedAt}
	if ev.CreatedAt.Time().After(p.lastEventAt) {
		p.lastEventAt = ev.CreatedAt.Time().UTC()
	}
	p.lastIngestedAt = ingestedAt
}

func (p *nostrFleetHealthProjector) observeSubscriptionStart() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.subscriptionActive = true
	p.caughtUp = false
}

func (p *nostrFleetHealthProjector) observeEOSE() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.subscriptionActive = true
	p.caughtUp = true
}

func (p *nostrFleetHealthProjector) observeSubscriptionEnd() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.subscriptionActive = false
	p.caughtUp = false
}

func (p *nostrFleetHealthProjector) observeRelayClosed() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.relayClosedTotal++
}

func (p *nostrFleetHealthProjector) snapshot(now time.Time) NostrFleetHealthSnapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := NostrFleetHealthSnapshot{SubscriptionActive: p.subscriptionActive, CaughtUp: p.caughtUp, LastEventAt: p.lastEventAt, LastIngestedAt: p.lastIngestedAt, RelayClosedTotal: p.relayClosedTotal, ProjectionErrors: p.projectionErrors, Entities: map[string]int{}, HeartbeatLagSeconds: map[string]float64{}}
	for _, entity := range p.entities {
		out.Entities[entity.domain+":"+entity.status]++
		if entity.domain == "agent" {
			lag := now.Sub(entity.eventAt).Seconds()
			if lag < 0 {
				lag = 0
			}
			out.HeartbeatLagSeconds[entity.label] = lag
		}
	}
	return out
}

func classifyFleetHealthEvent(ev *gonostr.Event) (domain, status, key string, class fleetHealthClass) {
	kind := int(ev.Kind)
	domain = boundedFleetDomain(tagValue(ev, "domain"), kind)
	status = boundedFleetStatus(tagValue(ev, "status"))
	if status == "unknown" && strings.TrimSpace(ev.Content) != "" {
		var payload map[string]any
		if json.Unmarshal([]byte(ev.Content), &payload) == nil {
			for _, field := range []string{"status", "health", "state"} {
				if value, exists := payload[field].(string); exists {
					status = boundedFleetStatus(value)
					if status != "unknown" {
						break
					}
				}
			}
		}
	}
	coordinate := tagValue(ev, "d")
	if domain == fleetDomainRoute {
		// Route entities are addressable: exactly one per managed-route
		// coordinate, carried by the replaceable status and state observables. A
		// route audit fact records a transition rather than current route state,
		// and a route observable without a coordinate cannot be attributed to any
		// route, so neither may fall back to a signer-wide entity that would
		// inflate the route count.
		if kind == kinds.CASAudit {
			return domain, status, "", fleetHealthLineage
		}
		if coordinate == "" {
			return "", "", "", fleetHealthInvalid
		}
	}
	if coordinate == "" {
		coordinate = ev.PubKey.Hex()
	}
	if coordinate == "" {
		return "", "", "", fleetHealthInvalid
	}
	key = domain + ":" + ev.PubKey.Hex() + ":" + coordinate
	return domain, status, key, fleetHealthEntity
}

func tagValue(ev *gonostr.Event, name string) string {
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == name {
			return strings.TrimSpace(tag[1])
		}
	}
	return ""
}

func boundedFleetDomain(value string, kind int) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	for _, known := range nostrFleetHealthDomains {
		if normalized == known {
			return known
		}
	}
	switch kind {
	case kinds.AssistantTranscript, kinds.SoulFactoryRuntimeCapability:
		return "agent"
	case kinds.CASAudit:
		return "control_plane"
	case kinds.CASControlState:
		return "service"
	default:
		return "runtime"
	}
}

func boundedFleetStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "healthy", "online", "active", "ok", "succeeded", "in_sync":
		return "healthy"
	case "degraded", "drifted", "stale", "pending":
		return "degraded"
	case "unhealthy", "offline", "failed", "error", "revoked":
		return "unhealthy"
	default:
		return "unknown"
	}
}

// ObserveNostrEvent projects a validated, persisted canonical observable.
func (p *Provider) ObserveNostrEvent(ctx context.Context, ev *gonostr.Event) {
	p.nostrFleetHealth.observeEvent(ctx, ev)
}
func (p *Provider) ObserveSubscriptionStart()      { p.nostrFleetHealth.observeSubscriptionStart() }
func (p *Provider) ObserveSubscriptionEnd()        { p.nostrFleetHealth.observeSubscriptionEnd() }
func (p *Provider) ObserveEOSE()                   { p.nostrFleetHealth.observeEOSE() }
func (p *Provider) ObserveRelayClosed(_, _ string) { p.nostrFleetHealth.observeRelayClosed() }
