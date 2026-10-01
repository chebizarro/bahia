package service

import (
	"context"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	sbomadapter "github.com/openagentsinc/bahia/internal/adapters/sbom"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// bahia-irsry.40: producers that record an event as queued move it to
// published when the outbox later delivers it (OnDelivered), and apply an
// outcome the outbox reached before they had stored the event id.

func securityObservable(d string) *nostr.Event {
	return &nostr.Event{Kind: KindSecuritySummary, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", d}, {"domain", "security"}}}
}

func publicationStates(repo *memorySecurityRepo) map[string]domain.SecurityPublicationState {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	states := map[string]domain.SecurityPublicationState{}
	for _, publication := range repo.publications {
		states[publication.DTag] = publication.PublishState
	}
	return states
}

func TestSecurityScannerPublishesQueuedPublicationWhenTheOutboxDeliversIt(t *testing.T) {
	ctx := context.Background()
	publisher := newQueuedSignedPublisher()
	runID := uuid.New()
	target := domain.SecurityTarget{ID: uuid.New(), TargetKeyHash: "target-hash", Type: domain.SecurityTargetSBOM}
	repo := newMemorySecurityRepo(target, &domain.SecurityScanRun{ID: runID, TargetKeyHash: "target-hash", PublishState: domain.SecurityPublicationPending})
	scanner := &SecurityScanner{repo: repo, publisher: publisher, pubkey: publisher.pubkey(), logger: zap.NewNop()}
	run := &domain.SecurityScanRun{ID: runID}

	require.NoError(t, scanner.publishObservable(ctx, run, &target, nil, "summary", SecurityScanSummarySchema, "summary", securityObservable("summary")))
	require.NoError(t, scanner.publishObservable(ctx, run, &target, nil, "audit", SecurityAuditSchema, "audit", securityObservable("audit")))
	require.Equal(t, map[string]domain.SecurityPublicationState{"summary": domain.SecurityPublicationPending, "audit": domain.SecurityPublicationPending}, publicationStates(repo))

	other := nostr.Event{Kind: KindSecuritySummary, Tags: nostr.Tags{{"domain", "sbom"}}}
	scanner.HandlePublishDelivered(other)
	require.Equal(t, domain.SecurityPublicationPending, publicationStates(repo)["summary"], "another producer's delivery is ignored")

	scanner.HandlePublishDelivered(publisher.events[0])
	require.Equal(t, domain.SecurityPublicationPublished, publicationStates(repo)["summary"])
	repo.mu.Lock()
	require.Equal(t, domain.SecurityPublicationPending, repo.runs[runID].PublishState, "the run waits for its other publication")
	repo.mu.Unlock()

	scanner.HandlePublishDelivered(publisher.events[1])
	scanner.HandlePublishDelivered(publisher.events[1]) // reported at least once
	require.Equal(t, domain.SecurityPublicationPublished, publicationStates(repo)["audit"])
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Equal(t, domain.SecurityPublicationPublished, repo.runs[runID].PublishState)
}

// The outbox may settle an event before the scanner stored its id, when the
// hooks find no row to update; the scanner reads the outcome right after.
func TestSecurityScannerAppliesAnOutcomeReachedBeforeTheEventIDWasStored(t *testing.T) {
	for _, tc := range []struct {
		outcome nostrutil.DeliveryOutcome
		want    domain.SecurityPublicationState
	}{
		{nostrutil.DeliveryPending, domain.SecurityPublicationPending},
		{nostrutil.DeliveryDelivered, domain.SecurityPublicationPublished},
		{nostrutil.DeliveryAbandoned, domain.SecurityPublicationFailedTerminal},
	} {
		t.Run(tc.outcome.String(), func(t *testing.T) {
			publisher := newQueuedSignedPublisher()
			publisher.outcome = tc.outcome
			target := domain.SecurityTarget{ID: uuid.New(), TargetKeyHash: "target-hash", Type: domain.SecurityTargetSBOM}
			repo := newMemorySecurityRepo(target, nil)
			scanner := &SecurityScanner{repo: repo, publisher: publisher, pubkey: publisher.pubkey(), logger: zap.NewNop()}

			require.NoError(t, scanner.publishObservable(context.Background(), nil, &target, nil, "summary", SecurityScanSummarySchema, "summary", securityObservable("summary")))
			require.Equal(t, tc.want, publicationStates(repo)["summary"])
		})
	}
}

// A manifest recorded as pending on a queued reference is published when the
// outbox delivers the reference, and so is the cached run result once none of
// its references is still queued.
func TestSBOMOrchestratorPublishesPendingManifestWhenTheOutboxDeliversTheReference(t *testing.T) {
	repo := newFakeSBOMManifestRepo()
	orchestrator := &SBOMOrchestrator{Repo: repo, results: map[string]SBOMRunResult{}}
	first := nostr.Event{Kind: sbomadapter.KindSBOMReference, Tags: nostr.Tags{{"d", "sbom:ref:1"}}}
	require.NoError(t, first.Sign(nostr.Generate()))
	second := nostr.Event{Kind: sbomadapter.KindSBOMReference, Tags: nostr.Tags{{"d", "sbom:ref:2"}}}
	require.NoError(t, second.Sign(nostr.Generate()))
	repo.projected = []domain.SBOMManifest{
		{ID: uuid.New(), ReferenceEventID: first.ID.Hex(), PublishState: domain.SBOMPublishPending},
		{ID: uuid.New(), ReferenceEventID: second.ID.Hex(), PublishState: domain.SBOMPublishPending},
	}
	refs := []string{first.ID.Hex(), second.ID.Hex()}
	orchestrator.rememberQueued("run-1", SBOMRunResult{RunID: "run-1", ReferenceEventIDs: refs, PublishState: domain.SBOMPublishPending}, refs)

	status := nostr.Event{Kind: KindSBOMStatus, Tags: nostr.Tags{{"d", "sbom:status:x"}}}
	orchestrator.HandlePublishDelivered(status)
	require.Equal(t, domain.SBOMPublishPending, repo.projected[0].PublishState, "status events are not references")

	orchestrator.HandlePublishDelivered(first)
	require.Equal(t, domain.SBOMPublishPublished, repo.projected[0].PublishState)
	require.Equal(t, domain.SBOMPublishPending, repo.projected[1].PublishState)
	cached, ok := orchestrator.cached("run-1")
	require.True(t, ok)
	require.Equal(t, domain.SBOMPublishPending, cached.PublishState, "one reference is still queued")

	orchestrator.HandlePublishDelivered(second)
	require.Equal(t, domain.SBOMPublishPublished, repo.projected[1].PublishState)
	cached, ok = orchestrator.cached("run-1")
	require.True(t, ok)
	require.Equal(t, domain.SBOMPublishPublished, cached.PublishState)
}

func TestSBOMOrchestratorAppliesAReferenceOutcomeReachedBeforeTheManifestWasStored(t *testing.T) {
	for _, tc := range []struct {
		outcome nostrutil.DeliveryOutcome
		want    domain.SBOMPublishState
	}{
		{nostrutil.DeliveryPending, domain.SBOMPublishPending},
		{nostrutil.DeliveryDelivered, domain.SBOMPublishPublished},
		{nostrutil.DeliveryAbandoned, domain.SBOMPublishFailed},
	} {
		t.Run(tc.outcome.String(), func(t *testing.T) {
			publisher := newQueuedSignedPublisher()
			publisher.outcome = tc.outcome
			repo := newFakeSBOMManifestRepo()
			orchestrator := &SBOMOrchestrator{Repo: repo, Publisher: publisher, results: map[string]SBOMRunResult{}}
			reference := nostr.Event{Kind: sbomadapter.KindSBOMReference, Tags: nostr.Tags{{"d", "sbom:ref:x"}}}
			require.NoError(t, reference.Sign(nostr.Generate()))
			repo.projected = []domain.SBOMManifest{{ID: uuid.New(), ReferenceEventID: reference.ID.Hex(), PublishState: domain.SBOMPublishPending}}

			orchestrator.applyDeliveryOutcome(context.Background(), reference.ID.Hex())
			require.Equal(t, tc.want, repo.projected[0].PublishState)
		})
	}
}
