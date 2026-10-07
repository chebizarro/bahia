package nostr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

// Audit facts. Every audited bus event is one immutable fact on
// the regular kind 4903: no d tag, so repeated audits of one entity coexist
// instead of replacing each other the way the retired addressable 31000-31099
// kinds did. A fact is correlated by tags rather than a coordinate:
//
//   - state=<cp-state d of the audited entity> (docs/event-spec.md),
//   - t=cp-audit (single-letter topic) plus t=<event type>,
//   - e=<source Nostr event id> when the bus payload names one,
//   - fact=<sha256(type, entity, content)>, the deterministic source-fact id.
//
// The fact id makes publishing idempotent per source fact: a republished bus
// event (handler retry, replay after restart) carries the same id, and the
// projector signs each fact id once. Distinct facts differ in type, entity or
// payload and so get distinct ids.

// auditFactCapacity bounds the remembered fact ids; it is also the number of
// retained audit records read back on hydration.
const auditFactCapacity = 10000

// auditFactSet is a bounded FIFO set of fact ids. It is guarded by
// projectionState.mu.
type auditFactSet struct {
	seen  map[string]struct{}
	order []string
}

func (f *auditFactSet) has(id string) bool {
	_, ok := f.seen[id]
	return ok
}

func (f *auditFactSet) add(id string) {
	if id == "" || f.has(id) {
		return
	}
	if f.seen == nil {
		f.seen = map[string]struct{}{}
	}
	f.seen[id] = struct{}{}
	f.order = append(f.order, id)
	for len(f.order) > auditFactCapacity {
		delete(f.seen, f.order[0])
		f.order = f.order[1:]
	}
}

func (f *auditFactSet) remove(id string) {
	delete(f.seen, id)
}

// claimAuditFact reserves fact for signing. It returns false when the fact was
// already signed, so the caller must not publish it again.
func (p *Projector) claimAuditFact(fact string) bool {
	s := p.projection()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.auditFacts.has(fact) {
		return false
	}
	s.auditFacts.add(fact)
	return true
}

// releaseAuditFact undoes a claim whose publish failed, so a retry can sign it.
func (p *Projector) releaseAuditFact(fact string) {
	s := p.projection()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auditFacts.remove(fact)
}

func (p *Projector) publishAudit(ctx context.Context, e events.Event) error {
	if !isAuditedEvent(e.Type) {
		return nil
	}
	res := resourceFromEvent(e)
	content, err := json.Marshal(map[string]any{
		"event_type": string(e.Type),
		"entity_id":  e.EntityID,
		"data":       e.Data,
	})
	if err != nil {
		return fmt.Errorf("marshal audit fact: %w", err)
	}
	fact := auditFactID(e.Type, e.EntityID, content)

	// Retained facts from before a restart must suppress a replayed one. A
	// retained-state read failure must not lose the audit, so publish anyway.
	if err := p.hydrateProjectionCache(ctx, KindCASAudit); err != nil {
		p.logger.Debug("audit fact dedupe cache unavailable; publishing without it", zap.String("event_type", string(e.Type)), zap.Error(err))
	}
	if !p.claimAuditFact(fact) {
		p.projection().count(projectionFamilyAudit, func(m *ProjectionFamilyMetrics) { m.Deduped++ })
		return nil
	}

	tags := gonostr.Tags{
		{"domain", auditDomainForEvent(e.Type)},
		{"type", string(e.Type)},
		{"schema", "bahia.audit.v1"},
		{"protected", "true"},
		{"t", kinds.CPAuditTopic},
		{"t", string(e.Type)},
		{"event_type", string(e.Type)},
		{kinds.CPAuditTagFact, fact},
	}
	if state := auditStateCoordinate(e, res); state != "" {
		tags = append(tags, gonostr.Tag{kinds.CPAuditTagState, state})
	}
	if source := auditSourceEventID(e.Data); source != "" {
		tags = append(tags, gonostr.Tag{"e", source})
	}
	tags = appendResourceTags(tags, res)
	tags = appendDNSAuditTags(tags, e.Data)
	if err := p.publishSigned(ctx, KindCASAudit, tags, string(content), string(e.Type), auditEntityID(e.Type, e.EntityID, res)); err != nil {
		p.releaseAuditFact(fact)
		return err
	}
	return nil
}

// auditFactID is the deterministic id of one source fact: the event type,
// entity and the canonical JSON content (map keys are sorted by encoding/json).
func auditFactID(t events.EventType, entityID string, content []byte) string {
	h := sha256.New()
	h.Write([]byte(t))
	h.Write([]byte{0})
	h.Write([]byte(entityID))
	h.Write([]byte{0})
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}

// auditStateCoordinate is the d tag of the cp-state record the fact is about:
// the service or LLM-route state coordinate for runtime facts, else the entity
// id, which is the d of the entity's registry record.
func auditStateCoordinate(e events.Event, res events.ResourceData) string {
	switch e.Type {
	case events.EventRuntimeDeploy, events.EventRuntimeRestart, events.EventRuntimeStop, events.EventAdoptionImported:
		serviceID, serviceOK := parseUUID(res.ServiceID)
		envID, envOK := parseUUID(res.EnvironmentID)
		if serviceOK && envOK {
			return serviceStateDTag(serviceID, envID)
		}
	}
	return strings.TrimSpace(e.EntityID)
}

// auditSourceEventID returns the Nostr event id the bus payload names as the
// fact's source (a ContextVM request, an upstream observation), if any.
func auditSourceEventID(data any) string {
	for _, key := range []string{"source_event_id", "request_event_id", "event_id"} {
		if id := strings.ToLower(strings.TrimSpace(stringifyMapValue(data, key))); isHexEventID(id) {
			return id
		}
	}
	return ""
}

func isHexEventID(id string) bool {
	if len(id) != 64 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// isAuditedEvent reports whether the projector records e as an audit fact:
// operator-meaningful, discrete mutations. Observation/sync/state-changed/
// drift events are not audited — their state is published as replaceable
// cp-state by the mutation-site publishers, and auditing them would add
// ~1 440 noise events/day.
func isAuditedEvent(t events.EventType) bool {
	switch t {
	case events.EventBuildRegistered, events.EventArtifactRegistered,
		events.EventDeploymentIntentCreated, events.EventDeploymentIntentApproved, events.EventDeploymentIntentRejected,
		events.EventDeploymentRunCreated, events.EventDeploymentRunCompleted,
		events.EventEnvironmentCreated, events.EventEnvironmentUpdated, events.EventEnvironmentDeleted,
		events.EventRuntimeDeploy, events.EventRuntimeRestart, events.EventRuntimeStop,
		events.EventAdoptionImported,
		events.EventLLMRouteCreated, events.EventLLMRouteUpdated, events.EventLLMReleaseRegistered,
		events.EventLLMDeploymentIntentCreated, events.EventLLMDeploymentIntentApproved, events.EventLLMDeploymentIntentRejected,
		events.EventLLMDeploymentRunCreated, events.EventLLMDeploymentRunCompleted,
		eventDNSEndpointRegistered, eventDNSEndpointDeregistered:
		return true
	default:
		return false
	}
}
