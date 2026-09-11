package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
)

const (
	// RouteCanaryNostrDomain is the CAS `domain` tag for managed-route canary
	// observables. Fleet-health telemetry counts it as its own domain, separate
	// from the `runtime` domain of the containers behind a route. The hostname
	// travels in a `hostname` tag because the Cascadia `route` tag is registered
	// for LLM/API route identifiers.
	RouteCanaryNostrDomain = "route"

	routeCanaryNostrEntity  = "route-canary"
	routeCanaryStatusSchema = "bahia.status.route-canary.v1"
	routeCanaryStateSchema  = "bahia.state.route-canary.v1"
	routeCanaryAuditSchema  = "bahia.audit.route-canary.v1"
)

// Bounded fleet-health statuses a managed route projects to. They are the
// normalized vocabulary fleet-health telemetry classifies by `#status`.
const (
	RouteCanaryFleetHealthy   = "healthy"
	RouteCanaryFleetDegraded  = "degraded"
	RouteCanaryFleetUnhealthy = "unhealthy"
	RouteCanaryFleetUnknown   = "unknown"
)

// routeCanaryProjectedEventTypes are the supervisor transitions the projector
// consumes, mapped to the route transition each one announces.
var routeCanaryProjectedEventTypes = map[events.EventType]domain.RouteCanaryTransition{
	events.EventRouteCanaryOutageOpened:          domain.RouteCanaryTransitionOpened,
	events.EventRouteCanaryRecovered:             domain.RouteCanaryTransitionRecovered,
	events.EventRouteCanaryClassificationChanged: domain.RouteCanaryTransitionClassificationChanged,
}

// RouteCanaryProjector projects route canary transitions to canonical durable
// Nostr observables so a route outage is visible to Nostr-native consumers and
// to fleet-health telemetry, not only to the REST API and in-process events.
//
// Each transition publishes the route's current state as NIP-38 status (30315)
// and CAS control state (30900), both addressed by the route coordinate, plus an
// immutable CAS audit fact (4903) for the transition itself.
type RouteCanaryProjector struct {
	publisher NostrEventPublisher
	logger    *zap.Logger

	mu sync.Mutex
	// published holds, per (kind, route coordinate) slot, the last event a relay
	// accepted. It makes redelivery idempotent and keeps an older transition
	// from overwriting newer replaceable state, while staying bounded by the
	// number of managed routes rather than growing with every transition.
	published map[string]routeCanaryPublished
}

type routeCanaryPublished struct {
	createdAt   gonostr.Timestamp
	fingerprint string
}

// NewRouteCanaryProjector subscribes a projector to route canary transitions.
func NewRouteCanaryProjector(bus events.Publisher, publisher NostrEventPublisher, logger *zap.Logger) (*RouteCanaryProjector, error) {
	if bus == nil {
		return nil, fmt.Errorf("route canary projector requires an event bus")
	}
	if publisher == nil {
		return nil, fmt.Errorf("route canary projector requires a Nostr publisher")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	p := &RouteCanaryProjector{
		publisher: publisher,
		logger:    logger.Named("route-canary-projector"),
		published: map[string]routeCanaryPublished{},
	}
	for typ := range routeCanaryProjectedEventTypes {
		if subscriber, ok := bus.(events.ErrorSubscriber); ok {
			// Returned errors make the bus retry, and only relay-accepted events
			// are recorded as published, so a failed publish is retried rather
			// than suppressed.
			subscriber.SubscribeWithError(typ, p.handle)
			continue
		}
		bus.Subscribe(typ, func(ctx context.Context, e events.Event) {
			if err := p.handle(ctx, e); err != nil {
				p.logger.Error("project route canary event", zap.String("event_type", string(e.Type)), zap.Error(err))
			}
		})
	}
	return p, nil
}

func (p *RouteCanaryProjector) handle(ctx context.Context, e events.Event) error {
	transition, ok := routeCanaryProjectedEventTypes[e.Type]
	if !ok {
		return fmt.Errorf("unsupported route canary event type %q", e.Type)
	}
	var payload RouteCanaryChanged
	switch v := e.Data.(type) {
	case RouteCanaryChanged:
		payload = v
	case *RouteCanaryChanged:
		if v == nil {
			return fmt.Errorf("route canary event %q has a nil payload", e.Type)
		}
		payload = *v
	default:
		return fmt.Errorf("unsupported route canary event payload %T", e.Data)
	}
	obs, err := newRouteCanaryObservation(e.Type, transition, payload)
	if err != nil {
		return err
	}
	return p.publish(ctx, obs)
}

// routeCanaryObservation is one sanitized, projection-ready route transition.
type routeCanaryObservation struct {
	eventType      events.EventType
	transition     domain.RouteCanaryTransition
	eventID        string
	state          domain.RouteCanaryState
	lineage        domain.RouteCanaryEvent
	instanceStatus domain.InstanceHealthStatus
	severity       domain.AlertSeverity
	reason         string
	occurredAt     time.Time
}

func newRouteCanaryObservation(eventType events.EventType, transition domain.RouteCanaryTransition, payload RouteCanaryChanged) (routeCanaryObservation, error) {
	state := payload.State
	if state.ServiceID == uuid.Nil || state.EnvironmentID == uuid.Nil || strings.TrimSpace(state.Hostname) == "" {
		return routeCanaryObservation{}, fmt.Errorf("route canary event %q has no route key", eventType)
	}
	occurredAt := payload.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = payload.Event.ObservedAt
	}
	if occurredAt.IsZero() {
		return routeCanaryObservation{}, fmt.Errorf("route canary event %q for %s has no occurrence time", eventType, state.Coordinate())
	}
	instanceStatus := payload.ObservedInstanceStatus
	if instanceStatus == "" {
		instanceStatus = payload.Event.ObservedInstanceStatus
	}
	// Evidence is sanitized upstream; sanitize again at the publication
	// boundary because relays are a wider audience than the database.
	state.FailureReason = domain.SanitizeEvidence(state.FailureReason)
	lineage := payload.Event
	lineage.Reason = domain.SanitizeEvidence(lineage.Reason)
	lineage.Evidence = domain.SanitizeEvidence(lineage.Evidence)
	return routeCanaryObservation{
		eventType:      eventType,
		transition:     transition,
		eventID:        payload.EventID,
		state:          state,
		lineage:        lineage,
		instanceStatus: instanceStatus,
		severity:       payload.Severity,
		reason:         domain.SanitizeEvidence(payload.Reason),
		occurredAt:     occurredAt,
	}, nil
}

// RouteCanaryFleetStatus maps durable route state to the bounded fleet-health
// status.
//
// An open outage is unhealthy whatever the latest classification, because the
// outage stays declared until the recovery threshold is met. A route failing
// below the open threshold is degraded: the failure is real but not yet a
// declared outage. Warning classifications (tls_expiring,
// health_path_not_discriminating) are degraded too, since the route still
// serves traffic but needs operator attention.
func RouteCanaryFleetStatus(state domain.RouteCanaryState) string {
	switch {
	case state.Open:
		return RouteCanaryFleetUnhealthy
	case !state.Classification.Valid():
		return RouteCanaryFleetUnknown
	case state.ConsecutiveFailures > 0:
		// ConsecutiveFailures is target-aware, so it also covers a warning the
		// operator promoted to a failure for this route.
		return RouteCanaryFleetDegraded
	case state.Classification == domain.RouteCanaryClassificationRouteOK:
		return RouteCanaryFleetHealthy
	default:
		return RouteCanaryFleetDegraded
	}
}

// RouteCanaryServiceHealthyRouteBroken reports the "service up, route down"
// contrast: a declared route outage while the containers behind the route are
// healthy or running. It matches the REST API's service_healthy_route_broken.
func RouteCanaryServiceHealthyRouteBroken(open bool, instance domain.InstanceHealthStatus) bool {
	return open && (instance == domain.InstanceHealthStatusHealthy || instance == domain.InstanceHealthStatusRunning)
}

// routeCanaryProjection is the JSON content shared by all three observables.
type routeCanaryProjection struct {
	Schema                    string                           `json:"schema"`
	EventID                   string                           `json:"event_id,omitempty"`
	Type                      events.EventType                 `json:"type"`
	Transition                domain.RouteCanaryTransition     `json:"transition"`
	Coordinate                string                           `json:"coordinate"`
	Status                    string                           `json:"status"`
	OutageOpen                bool                             `json:"outage_open"`
	Classification            domain.RouteCanaryClassification `json:"classification"`
	PreviousClassification    domain.RouteCanaryClassification `json:"previous_classification,omitempty"`
	Perspective               domain.RouteCanaryPerspective    `json:"perspective,omitempty"`
	Severity                  domain.AlertSeverity             `json:"severity,omitempty"`
	Reason                    string                           `json:"reason,omitempty"`
	ObservedInstanceStatus    domain.InstanceHealthStatus      `json:"observed_instance_status,omitempty"`
	ServiceHealthyRouteBroken bool                             `json:"service_healthy_route_broken"`
	OccurredAt                time.Time                        `json:"occurred_at"`
	// RouteCanary is the full durable route state, carried by the 30900 state.
	RouteCanary *domain.RouteCanaryState `json:"route_canary,omitempty"`
	// Evidence is the per-perspective probe summary, carried by the 4903 audit.
	Evidence string `json:"evidence,omitempty"`
}

func (o routeCanaryObservation) content(schema string) routeCanaryProjection {
	return routeCanaryProjection{
		Schema:                    schema,
		EventID:                   o.eventID,
		Type:                      o.eventType,
		Transition:                o.transition,
		Coordinate:                o.state.Coordinate(),
		Status:                    RouteCanaryFleetStatus(o.state),
		OutageOpen:                o.state.Open,
		Classification:            o.state.Classification,
		PreviousClassification:    o.lineage.PreviousClassification,
		Perspective:               o.state.Perspective,
		Severity:                  o.severity,
		Reason:                    o.reason,
		ObservedInstanceStatus:    o.instanceStatus,
		ServiceHealthyRouteBroken: RouteCanaryServiceHealthyRouteBroken(o.state.Open, o.instanceStatus),
		OccurredAt:                o.occurredAt.UTC(),
	}
}

// routeCanaryOutbound is one event plus the dedupe slot it occupies.
type routeCanaryOutbound struct {
	event       gonostr.Event
	slot        string
	replaceable bool
}

func (o routeCanaryObservation) outbound() ([]routeCanaryOutbound, error) {
	coordinate := o.state.Coordinate()
	createdAt := o.occurredAt.Unix()

	status := o.content(routeCanaryStatusSchema)
	state := o.content(routeCanaryStateSchema)
	sanitizedState := o.state
	state.RouteCanary = &sanitizedState
	audit := o.content(routeCanaryAuditSchema)
	audit.Evidence = o.lineage.Evidence

	auditTags := append(gonostr.Tags{
		{"domain", RouteCanaryNostrDomain},
		{"schema", routeCanaryAuditSchema},
		{"type", string(o.eventType)},
		{"transition", string(o.transition)},
		// The state tag names the 30900 coordinate this fact is about.
		{"state", coordinate},
	}, routeCanaryResourceTags(o.state.RouteCanaryKey)...)

	specs := []struct {
		kind        int
		tags        gonostr.Tags
		content     routeCanaryProjection
		replaceable bool
	}{
		{kinds.NIP38Status, routeCanaryAddressTags(o.state.RouteCanaryKey, routeCanaryStatusSchema), status, true},
		{kinds.CASControlState, routeCanaryAddressTags(o.state.RouteCanaryKey, routeCanaryStateSchema), state, true},
		{kinds.CASAudit, auditTags, audit, false},
	}
	out := make([]routeCanaryOutbound, 0, len(specs))
	for _, spec := range specs {
		encoded, err := json.Marshal(spec.content)
		if err != nil {
			return nil, fmt.Errorf("encode route canary kind %d: %w", spec.kind, err)
		}
		tags := append(spec.tags, o.observationTags()...)
		out = append(out, routeCanaryOutbound{
			event:       gonostr.Event{Kind: gonostr.Kind(spec.kind), CreatedAt: gonostr.Timestamp(createdAt), Tags: tags, Content: string(encoded)},
			slot:        fmt.Sprintf("%d|%s", spec.kind, coordinate),
			replaceable: spec.replaceable,
		})
	}
	return out, nil
}

// observationTags are the filterable facts every route observable carries,
// including the observed container status so the service-up/route-down
// contrast is visible without decoding content.
func (o routeCanaryObservation) observationTags() gonostr.Tags {
	tags := gonostr.Tags{
		{"status", RouteCanaryFleetStatus(o.state)},
		{"outage", routeCanaryOutageTag(o.state.Open)},
		{"classification", string(o.state.Classification)},
	}
	if o.state.Perspective != "" {
		tags = append(tags, gonostr.Tag{"perspective", string(o.state.Perspective)})
	}
	if o.instanceStatus != "" {
		tags = append(tags, gonostr.Tag{"instance_status", string(o.instanceStatus)})
	}
	return append(tags, gonostr.Tag{"service_healthy_route_broken", strconv.FormatBool(RouteCanaryServiceHealthyRouteBroken(o.state.Open, o.instanceStatus))})
}

func routeCanaryOutageTag(open bool) string {
	if open {
		return "open"
	}
	return "closed"
}

// routeCanaryAddressTags addresses a route's replaceable observables by the same
// coordinate the REST API, lineage and in-process events use.
func routeCanaryAddressTags(key domain.RouteCanaryKey, schema string) gonostr.Tags {
	return append(gonostr.Tags{
		{"d", key.Coordinate()},
		{"domain", RouteCanaryNostrDomain},
		{"schema", schema},
		{"entity", routeCanaryNostrEntity},
	}, routeCanaryResourceTags(key)...)
}

func routeCanaryResourceTags(key domain.RouteCanaryKey) gonostr.Tags {
	tags := gonostr.Tags{{"service", key.ServiceID.String()}, {"environment", key.EnvironmentID.String()}}
	if key.DeploymentUnitID != nil {
		tags = append(tags, gonostr.Tag{"deployment_unit", key.DeploymentUnitID.String()})
	}
	return append(tags, gonostr.Tag{"hostname", key.Hostname})
}

// publish sends every observable for one transition and records each one only
// after the relay path accepted it. A failure on one event does not stop the
// others; all failures are returned together so the bus retries the
// transition, and already-accepted events are skipped on the retry.
func (p *RouteCanaryProjector) publish(ctx context.Context, obs routeCanaryObservation) error {
	outbound, err := obs.outbound()
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var errs []error
	for i := range outbound {
		item := &outbound[i]
		fingerprint, err := managedEventFingerprint(item.event)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if last, ok := p.published[item.slot]; ok {
			if last.fingerprint == fingerprint {
				continue
			}
			// A replaceable observable older than the one already accepted would
			// only be discarded by relays and consumers; audit facts are
			// immutable history and are always published.
			if item.replaceable && item.event.CreatedAt < last.createdAt {
				continue
			}
		}
		if err := p.publisher.PublishSignedEvent(ctx, &item.event); err != nil {
			errs = append(errs, fmt.Errorf("publish route canary kind %d for %s: %w", item.event.Kind, obs.state.Coordinate(), err))
			continue
		}
		p.published[item.slot] = routeCanaryPublished{createdAt: item.event.CreatedAt, fingerprint: fingerprint}
	}
	return errors.Join(errs...)
}
