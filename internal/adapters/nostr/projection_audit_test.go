package nostr

import (
	"context"
	"errors"
	"testing"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

func auditEvents(sink *captureProjectionPublisher) []gonostr.Event {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var out []gonostr.Event
	for _, ev := range sink.events {
		if eventKindInt(&ev) == KindCASAudit {
			out = append(out, ev)
		}
	}
	return out
}

// assertOneAudit returns the single audit fact recorded for eventType and
// checks the canonical fact shape: regular 4903, no d, no legacy kind, the
// cp-audit topic and a fact id.
func assertOneAudit(t *testing.T, sink *captureProjectionPublisher, eventType events.EventType) gonostr.Event {
	t.Helper()
	var found []gonostr.Event
	for _, ev := range auditEvents(sink) {
		if tagValue(ev.Tags, "type") == string(eventType) {
			found = append(found, ev)
		}
	}
	if len(found) != 1 {
		t.Fatalf("audit facts for %s = %d, want 1", eventType, len(found))
	}
	ev := found[0]
	if !ev.VerifySignature() {
		t.Fatalf("audit fact %s has an invalid signature", eventType)
	}
	assertCanonicalAuditFact(t, ev)
	return ev
}

func assertCanonicalAuditFact(t *testing.T, ev gonostr.Event) {
	t.Helper()
	if kind := eventKindInt(&ev); kind != kinds.CASAudit {
		t.Fatalf("audit wire kind = %d, want regular %d", kind, kinds.CASAudit)
	}
	for _, key := range []string{"d", "legacy_kind"} {
		if value := tagValue(ev.Tags, key); value != "" {
			t.Fatalf("audit fact carries %s=%q; facts are regular events", key, value)
		}
	}
	if !hasTag(ev.Tags, "t", kinds.CPAuditTopic) {
		t.Fatalf("audit fact missing t=%s: %v", kinds.CPAuditTopic, ev.Tags)
	}
	if len(tagValue(ev.Tags, kinds.CPAuditTagFact)) != 64 {
		t.Fatalf("audit fact id = %q, want a sha256 hex", tagValue(ev.Tags, kinds.CPAuditTagFact))
	}
	assertTag(t, ev, "schema", "bahia.audit.v1")
}

// Repeated audits of one entity are separate immutable facts that coexist on
// the same state coordinate; republishing a fact already signed emits nothing.
func TestProjectionAuditFactsCoexistAndRepublishIsIdempotent(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop())
	serviceID, envID := uuid.New(), uuid.New()
	drift := func(reason string) events.Event {
		return events.Event{Type: events.EventDriftDetected, EntityID: serviceID.String(), Data: map[string]string{
			"service_id": serviceID.String(), "environment_id": envID.String(), "reason": reason,
		}}
	}

	first, second := drift("image digest changed"), drift("replica count changed")
	for _, ev := range []events.Event{first, second, first} {
		if err := projector.publishAudit(ctx, ev); err != nil {
			t.Fatalf("publish audit: %v", err)
		}
	}

	facts := auditEvents(sink)
	if len(facts) != 2 {
		t.Fatalf("audit facts = %d, want 2 (two distinct drifts, republish deduped)", len(facts))
	}
	coordinate := serviceStateDTag(serviceID, envID)
	for _, fact := range facts {
		assertCanonicalAuditFact(t, fact)
		assertTag(t, fact, kinds.CPAuditTagState, coordinate)
		assertTag(t, fact, "type", string(events.EventDriftDetected))
		assertTag(t, fact, "service", serviceID.String())
	}
	if tagValue(facts[0].Tags, kinds.CPAuditTagFact) == tagValue(facts[1].Tags, kinds.CPAuditTagFact) {
		t.Fatal("distinct facts share a fact id")
	}
	if m := projector.ProjectionMetrics()[projectionFamilyAudit]; m.Accepted != 2 || m.Deduped != 1 {
		t.Fatalf("audit metrics = %+v, want accepted=2 deduped=1", m)
	}
}

// A restarted projector hydrates signed fact ids from the retained store, so
// replaying a bus event it already audited does not duplicate the fact.
func TestProjectionAuditFactRepublishAfterRestartIsIdempotent(t *testing.T) {
	ctx := context.Background()
	repo := &memoryNostrEventRepo{records: map[string]repository.NostrEventRecord{}}
	ev := events.Event{Type: events.EventEnvironmentUpdated, EntityID: uuid.NewString(), Data: map[string]any{"name": "prod"}}

	first := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), &captureProjectionPublisher{}, repo, zap.NewNop())
	if err := first.publishAudit(ctx, ev); err != nil {
		t.Fatal(err)
	}

	sink := &captureProjectionPublisher{}
	restarted := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repo, zap.NewNop())
	if err := restarted.publishAudit(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if got := len(auditEvents(sink)); got != 0 {
		t.Fatalf("restart re-signed %d audit facts, want 0", got)
	}
	ev.Data = map[string]any{"name": "api-v2"}
	if err := restarted.publishAudit(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if got := len(auditEvents(sink)); got != 1 {
		t.Fatalf("new fact after restart = %d events, want 1", got)
	}
}

// A fact whose publish failed is not remembered, so the retry signs it.
func TestProjectionAuditFactPublishFailureAllowsRetry(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{errorsByKind: map[int]error{KindCASAudit: errors.New("relay down")}}
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop())
	ev := events.Event{Type: events.EventEnvironmentCreated, EntityID: uuid.NewString()}

	if err := projector.publishAudit(ctx, ev); err == nil {
		t.Fatal("publish audit succeeded against a failing relay")
	}
	sink.mu.Lock()
	sink.errorsByKind = nil
	sink.mu.Unlock()
	projector.resetProjectionBackoff()
	if err := projector.publishAudit(ctx, ev); err != nil {
		t.Fatalf("retry: %v", err)
	}
	assertOneAudit(t, sink, events.EventEnvironmentCreated)
}

// The source Nostr event a bus payload names is carried as the e tag.
func TestProjectionAuditFactCarriesSourceEventID(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop())
	source := "ab0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd"
	ev := events.Event{Type: events.EventDeploymentIntentApproved, EntityID: uuid.NewString(), Data: map[string]any{"request_event_id": source}}
	if err := projector.publishAudit(ctx, ev); err != nil {
		t.Fatal(err)
	}
	fact := assertOneAudit(t, sink, events.EventDeploymentIntentApproved)
	assertTag(t, fact, "e", source)
	assertTag(t, fact, kinds.CPAuditTagState, ev.EntityID)
}

// An audit fact the outbox abandoned was never delivered, so the abandon hook
// releases its id and a republish signs it again.
func TestProjectionAuditFactAbandonedDeliveryAllowsRepublish(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop())
	ev := events.Event{Type: events.EventDeploymentRunStatusChanged, EntityID: uuid.NewString()}
	if err := projector.publishAudit(ctx, ev); err != nil {
		t.Fatal(err)
	}
	projector.ForgetAbandonedProjection(assertOneAudit(t, sink, events.EventDeploymentRunStatusChanged))
	if err := projector.publishAudit(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if got := len(auditEvents(sink)); got != 2 {
		t.Fatalf("audit facts after abandonment and republish = %d, want 2", got)
	}
}
