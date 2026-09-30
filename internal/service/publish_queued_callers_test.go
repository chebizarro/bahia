package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	sbomadapter "github.com/openagentsinc/bahia/internal/adapters/sbom"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// queuedPublish is what the outbox publisher returns below the publish quorum:
// the signed event is durable and still being retried.
var queuedPublish = fmt.Errorf("nostr event accepted by 0 of 1 required relays: %w", nostrutil.ErrPublishIncomplete)

// queuedSignedPublisher signs and records every event and reports it queued.
type queuedSignedPublisher struct {
	secret nostr.SecretKey
	events []nostr.Event
}

func newQueuedSignedPublisher() *queuedSignedPublisher {
	return &queuedSignedPublisher{secret: nostr.Generate()}
}

func (p *queuedSignedPublisher) PublishSignedEvent(_ context.Context, ev *nostr.Event) error {
	if err := ev.Sign(p.secret); err != nil {
		return err
	}
	p.events = append(p.events, *ev)
	return queuedPublish
}

func (p *queuedSignedPublisher) PublishSignedEventWithResults(ctx context.Context, ev *nostr.Event) ([]sbomadapter.PublishOKResult, error) {
	err := p.PublishSignedEvent(ctx, ev)
	return []sbomadapter.PublishOKResult{{RelayURL: "wss://down.example", Error: fmt.Errorf("connection refused")}}, err
}

func (p *queuedSignedPublisher) pubkey() string { return p.secret.Public().Hex() }

func TestBahiaStatusProjectorDoesNotResignQueuedPublish(t *testing.T) {
	ctx := context.Background()
	publisher := newQueuedSignedPublisher()
	projector := NewBahiaStatusProjector(publisher, nil, "bahia-instance-1")
	payload := BahiaIdentityPayload{Version: "v0.9.0", CatalogVersion: "c1", Mode: "emergency", StartedAt: 1779559200}

	require.NoError(t, projector.PublishIdentity(ctx, payload), "a queued publish is kept, not failed")
	require.NoError(t, projector.PublishIdentity(ctx, payload))
	require.Len(t, publisher.events, 1, "the unchanged status must not be re-signed while it is queued")
}

func TestManagedInstanceHealthProjectorDoesNotResignQueuedPublish(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	publisher := newQueuedSignedPublisher()
	p := NewManagedInstanceHealthProjector(nil, publisher, zap.NewNop())
	payload := ManagedInstanceHealthChanged{EventID: "evt-1", Health: domain.ManagedInstanceHealth{ManagedInstanceKey: testKey(), SupervisorType: domain.InstanceSupervisorDocker, Status: domain.InstanceHealthStatusUnhealthy, LastObservedAt: now}, PreviousStatus: domain.InstanceHealthStatusRunning, Severity: domain.AlertSeverityError, Alert: true, OccurredAt: now}
	event := events.Event{Type: events.EventRuntimeInstanceHealthChanged, Data: payload}

	require.NoError(t, p.handle(context.Background(), event), "a queued publish must not make the bus redeliver")
	require.Len(t, publisher.events, 3)
	require.NoError(t, p.handle(context.Background(), event))
	require.Len(t, publisher.events, 3, "a redelivered transition must not re-sign queued events")
}

func TestRouteCanaryProjectorDoesNotResignQueuedPublish(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	publisher := newQueuedSignedPublisher()
	_, bus := newTestRouteCanaryProjector(t, publisher)
	event := events.Event{Type: events.EventRouteCanaryOutageOpened, Data: routeProjectorPayload(domain.RouteCanaryTransitionOpened, true, domain.RouteCanaryClassificationConnectFailed, 3, domain.InstanceHealthStatusHealthy, now)}

	require.NoError(t, bus.deliver(context.Background(), event), "a queued publish must not surface as a bus retry")
	published := len(publisher.events)
	require.Positive(t, published)
	require.NoError(t, bus.deliver(context.Background(), event))
	require.Len(t, publisher.events, published, "a redelivered transition must not re-sign queued events")
}

func TestSBOMOrchestratorTreatsQueuedPublishAsKept(t *testing.T) {
	publisher := newQueuedSignedPublisher()
	orchestrator := &SBOMOrchestrator{Publisher: publisher, Pubkey: publisher.pubkey()}
	ev := &nostr.Event{Kind: KindSBOMStatus, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", "sbom:status:x"}}}

	id, err := orchestrator.publishVerified(context.Background(), ev, "SBOM status")
	require.NoError(t, err, "a queued SBOM event must not fail the run")
	require.Equal(t, ev.ID.Hex(), id)
	require.Len(t, publisher.events, 1)
}

func TestSecurityScannerRecordsQueuedPublishAsPending(t *testing.T) {
	publisher := newQueuedSignedPublisher()
	repo := newMemorySecurityRepo(domain.SecurityTarget{}, nil)
	scanner := &SecurityScanner{repo: repo, publisher: publisher, pubkey: publisher.pubkey(), logger: zap.NewNop()}
	ev := &nostr.Event{Kind: KindSecurityAudit, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", "security:audit:x"}}}

	require.NoError(t, scanner.publishObservable(context.Background(), nil, nil, nil, "audit", SecurityAuditSchema, "security:audit:x", ev))
	require.Len(t, publisher.events, 1)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.publications, 1)
	for _, publication := range repo.publications {
		require.Equal(t, domain.SecurityPublicationPending, publication.PublishState, "queued, not failed_retryable")
		require.Equal(t, ev.ID.Hex(), publication.EventID, "the queued event is tracked by ID, not re-signed")
	}
}
