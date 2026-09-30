package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	sbomadapter "github.com/openagentsinc/bahia/internal/adapters/sbom"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// abandonedPublish is what the outbox publisher returns when the first
// delivery round already made the publish quorum unreachable (every relay
// answered OK=false with blocked:/invalid:/pow:): the row is failed and
// nothing retries it.
var abandonedPublish = fmt.Errorf("nostr event delivery abandoned: accepted by 0 of 1 required relays: wss://relay.example rejected permanently: blocked: pubkey not allowed: %w", nostrutil.ErrPublishAbandoned)

// abandonedSignedPublisher signs every event and reports it abandoned.
type abandonedSignedPublisher struct {
	secret nostr.SecretKey
	events []nostr.Event
}

func (p *abandonedSignedPublisher) PublishSignedEventWithResults(_ context.Context, ev *nostr.Event) ([]sbomadapter.PublishOKResult, error) {
	if err := ev.Sign(p.secret); err != nil {
		return nil, err
	}
	p.events = append(p.events, *ev)
	return []sbomadapter.PublishOKResult{{RelayURL: "wss://relay.example", Reason: "blocked: pubkey not allowed"}}, abandonedPublish
}

func TestSecurityScannerRecordsFirstRoundAbandonedPublishAsTerminal(t *testing.T) {
	publisher := &abandonedSignedPublisher{secret: nostr.Generate()}
	repo := newMemorySecurityRepo(domain.SecurityTarget{}, nil)
	scanner := &SecurityScanner{repo: repo, publisher: publisher, pubkey: publisher.secret.Public().Hex(), logger: zap.NewNop()}
	ev := &nostr.Event{Kind: KindSecurityAudit, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", "security:audit:x"}, {"domain", "security"}}}

	err := scanner.publishObservable(context.Background(), nil, nil, nil, "audit", SecurityAuditSchema, "security:audit:x", ev)
	require.ErrorIs(t, err, nostrutil.ErrPublishAbandoned, "an abandoned observable fails the publish")
	require.False(t, nostrutil.IsPublishQueued(err))
	require.Len(t, publisher.events, 1)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.publications, 1)
	for _, publication := range repo.publications {
		require.Equal(t, domain.SecurityPublicationFailedTerminal, publication.PublishState, "abandoned, not pending")
		require.Equal(t, ev.ID.Hex(), publication.EventID, "the failed signed event stays traceable to its outbox row")
		require.Contains(t, publication.LastError, "blocked: pubkey not allowed")
	}
}

// A publication recorded as queued becomes failed_terminal, with its run's
// publish state, when the outbox runner abandons the event later.
func TestSecurityScannerRecordsRunnerAbandonmentOfQueuedPublication(t *testing.T) {
	publisher := newQueuedSignedPublisher()
	runID := uuid.New()
	target := domain.SecurityTarget{ID: uuid.New(), TargetKeyHash: "target-hash", Type: domain.SecurityTargetSBOM}
	repo := newMemorySecurityRepo(target, &domain.SecurityScanRun{ID: runID, TargetKeyHash: "target-hash", PublishState: domain.SecurityPublicationPublished})
	scanner := &SecurityScanner{repo: repo, publisher: publisher, pubkey: publisher.pubkey(), logger: zap.NewNop()}
	run := &domain.SecurityScanRun{ID: runID}
	ev := &nostr.Event{Kind: KindSecuritySummary, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", "security:summary:x"}, {"domain", "security"}}}

	require.NoError(t, scanner.publishObservable(context.Background(), run, &target, nil, "summary", SecurityScanSummarySchema, "security:summary:x", ev))
	require.True(t, repo.hasPublicationState(domain.SecurityPublicationPending))

	// Another producer's abandoned event on the same publisher is ignored.
	other := nostr.Event{Kind: KindSecuritySummary, Tags: nostr.Tags{{"domain", "sbom"}}}
	scanner.HandlePublishAbandoned(other)
	require.True(t, repo.hasPublicationState(domain.SecurityPublicationPending))

	scanner.HandlePublishAbandoned(publisher.events[0])
	require.False(t, repo.hasPublicationState(domain.SecurityPublicationPending), "no publication stays pending forever")
	require.True(t, repo.hasPublicationState(domain.SecurityPublicationFailedTerminal))
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Equal(t, domain.SecurityPublicationFailedTerminal, repo.runs[runID].PublishState, "the run no longer reads as published")
}

func TestSBOMOrchestratorFailsRunOnFirstRoundAbandonedPublish(t *testing.T) {
	publisher := &abandonedSignedPublisher{secret: nostr.Generate()}
	orchestrator := &SBOMOrchestrator{Publisher: publisher, Pubkey: publisher.secret.Public().Hex()}
	ev := &nostr.Event{Kind: sbomadapter.KindSBOMReference, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", "sbom:ref:x"}}}

	id, err := orchestrator.publishVerified(context.Background(), ev, "SBOM reference")
	require.ErrorIs(t, err, nostrutil.ErrPublishAbandoned, "an abandoned reference must not be recorded as published")
	require.Empty(t, id)
}

// A manifest recorded as published on a queued reference is marked failed,
// and its cached run result dropped, when the runner abandons the reference.
func TestSBOMOrchestratorFailsManifestWhenRunnerAbandonsReference(t *testing.T) {
	repo := newFakeSBOMManifestRepo()
	orchestrator := &SBOMOrchestrator{Repo: repo, results: map[string]SBOMRunResult{}}
	reference := nostr.Event{Kind: sbomadapter.KindSBOMReference, Tags: nostr.Tags{{"d", "sbom:ref:x"}}}
	require.NoError(t, reference.Sign(nostr.Generate()))
	repo.projected = []domain.SBOMManifest{
		{ID: uuid.New(), ReferenceEventID: reference.ID.Hex(), PublishState: domain.SBOMPublishPublished},
		{ID: uuid.New(), ReferenceEventID: "other", PublishState: domain.SBOMPublishPublished},
	}
	orchestrator.remember("run-1", SBOMRunResult{RunID: "run-1", ReferenceEventIDs: []string{reference.ID.Hex()}})
	orchestrator.remember("run-2", SBOMRunResult{RunID: "run-2", ReferenceEventIDs: []string{"other"}})

	status := nostr.Event{Kind: KindSBOMStatus, Tags: nostr.Tags{{"d", "sbom:status:x"}}}
	orchestrator.HandlePublishAbandoned(status)
	require.Equal(t, domain.SBOMPublishPublished, repo.projected[0].PublishState, "status events are not references")

	orchestrator.HandlePublishAbandoned(reference)
	require.Equal(t, domain.SBOMPublishFailed, repo.projected[0].PublishState)
	require.Contains(t, repo.projected[0].PublishError, "abandoned")
	require.Equal(t, domain.SBOMPublishPublished, repo.projected[1].PublishState)
	_, cached := orchestrator.cached("run-1")
	require.False(t, cached, "retrying the idempotency key must run again")
	_, cached = orchestrator.cached("run-2")
	require.True(t, cached)
}

// abandoningConfigPublisher behaves like the control-plane outbox when every
// relay rejects the event permanently in the first round: the row is failed
// and the call reports abandonment.
type abandoningConfigPublisher struct {
	repo repository.NostrEventOutboxRepository
}

func (p *abandoningConfigPublisher) PublishPresignedEvent(ctx context.Context, event nostr.Event, _ string) error {
	if err := p.repo.AbandonPublish(ctx, event.ID.Hex(), "abandoned: required relay acceptance is unreachable; blocked: no"); err != nil {
		return err
	}
	return abandonedPublish
}

func TestConfigFabricAbandonedPublishIsNotDesiredState(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewInMemoryNostrEventRepository()
	accepted := &configTestPublisher{}
	signer := newConfigTestSigner(t)
	svc := NewConfigFabricService(repo, accepted, signer)
	base := time.Unix(1787625660, 0)
	tick := 0
	svc.now = func() time.Time {
		tick++
		return base.Add(time.Duration(tick) * time.Second)
	}
	first, err := svc.Publish(ctx, validPolicyRequest(1))
	require.NoError(t, err)

	svc.publisher = &abandoningConfigPublisher{repo: repo}
	receipt, err := svc.Publish(ctx, validPolicyRequest(2))
	require.ErrorIs(t, err, nostrutil.ErrPublishAbandoned, "abandoned is a failure, not a queued receipt")
	require.Nil(t, receipt)

	drift, err := svc.ListDrift(ctx)
	require.NoError(t, err)
	require.Len(t, drift, 1)
	require.Equal(t, first.EventID, drift[0].DesiredEventID, "the abandoned version is not desired state")
	require.Equal(t, 1, drift[0].DesiredVersion)
	require.Len(t, drift[0].Versions, 1)
	require.Error(t, svc.requireMonotonicVersion(ctx, first.PubKey, validPolicyRequest(2)), "versions stay monotonic past an abandoned one")
}

// A version reported as queued that the outbox runner abandons later drops
// out of desired state: the service reads the outbox row's state.
func TestConfigFabricRunnerAbandonedVersionIsNotDesiredState(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewInMemoryNostrEventRepository()
	queued := &configTestPublisher{err: queuedPublish}
	svc := NewConfigFabricService(repo, queued, newConfigTestSigner(t))
	base := time.Unix(1787625660, 0)
	tick := 0
	svc.now = func() time.Time {
		tick++
		return base.Add(time.Duration(tick) * time.Second)
	}
	queued.err = nil
	first, err := svc.Publish(ctx, validPolicyRequest(1))
	require.NoError(t, err)
	queued.err = queuedPublish
	second, err := svc.Publish(ctx, validPolicyRequest(2))
	require.NoError(t, err)
	require.Equal(t, ConfigDeliveryQueued, second.Delivery)

	drift, err := svc.ListDrift(ctx)
	require.NoError(t, err)
	require.Equal(t, second.EventID, drift[0].DesiredEventID)

	require.NoError(t, repo.AbandonPublish(ctx, second.EventID, "abandoned after 30 publish attempts"))
	drift, err = svc.ListDrift(ctx)
	require.NoError(t, err)
	require.Equal(t, first.EventID, drift[0].DesiredEventID, "the runner-abandoned version is not desired state")
}
