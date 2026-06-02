package nostrmigration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	gonostr "github.com/nbd-wtf/go-nostr"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type captureMigrationPublisher struct {
	outcomes []PublishOutcome
	events   []gonostr.Event
}

type fakeMigrationSubscriber struct {
	filters [][]gonostr.Filter
	events  []*gonostr.Event
}

func (s *fakeMigrationSubscriber) SubscribeAllWithEOSE(_ context.Context, filters []gonostr.Filter) (*nostrAdapter.MergedSubscription, error) {
	s.filters = append(s.filters, filters)
	events := make(chan *gonostr.Event)
	eose := make(chan struct{})
	go func() {
		defer close(events)
		defer close(eose)
		for _, ev := range s.events {
			events <- ev
		}
	}()
	closed := make(chan nostrAdapter.RelayClosed)
	close(closed)
	relayEOSE := make(chan nostrAdapter.RelayEOSE)
	close(relayEOSE)
	return &nostrAdapter.MergedSubscription{Events: events, EndOfStoredEvents: eose, Closed: closed, RelayEOSE: relayEOSE}, nil
}

func (p *captureMigrationPublisher) PublishMigrationEvent(_ context.Context, ev gonostr.Event) ([]PublishOutcome, error) {
	p.events = append(p.events, ev)
	return p.outcomes, nil
}

func TestRunnerMigratesLocalLegacyRecordAndRecordsCanonicalEvent(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewInMemoryNostrEventRepository()
	legacy := legacyRecord(t, "legacy-1", kinds.DeployRequest, time.Unix(100, 0).UTC())
	inserted, err := repo.Record(ctx, legacy)
	require.NoError(t, err)
	require.True(t, inserted)
	publisher := &captureMigrationPublisher{outcomes: []PublishOutcome{{RelayURL: "wss://relay.example", Accepted: true}}}
	runner := NewRunner(repo, publisher, nil, Config{PrivateKey: deterministicPrivateKey(t)}, zap.NewNop())

	require.NoError(t, runner.Run(ctx))
	require.Len(t, publisher.events, 1)
	published := publisher.events[0]
	require.Equal(t, CanonicalContextVMMessage, published.Kind)
	require.NotEmpty(t, published.ID)
	require.Contains(t, tagValues(published.Tags, "migrated-from"), legacy.ID)
	require.Contains(t, tagValues(published.Tags, "legacy-kind"), "5961")
	require.Contains(t, tagValues(published.Tags, "method"), "service/deploy")

	found, err := repo.FindByTag(ctx, "migrated-from", legacy.ID, []int{CanonicalContextVMMessage}, 10)
	require.NoError(t, err)
	require.Len(t, found, 1)
}

func TestRunnerSkipsAlreadyMigratedLegacyRecord(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewInMemoryNostrEventRepository()
	legacy := legacyRecord(t, "legacy-1", kinds.ServiceState, time.Unix(100, 0).UTC())
	_, err := repo.Record(ctx, legacy)
	require.NoError(t, err)
	tags, err := json.Marshal(gonostr.Tags{{"migrated-from", legacy.ID}})
	require.NoError(t, err)
	_, err = repo.Record(ctx, &repository.NostrEventRecord{ID: "canonical-existing", Kind: CanonicalCASCPState, Tags: tags, CreatedAt: time.Unix(101, 0).UTC()})
	require.NoError(t, err)
	publisher := &captureMigrationPublisher{outcomes: []PublishOutcome{{Accepted: true}}}

	require.NoError(t, NewRunner(repo, publisher, nil, Config{PrivateKey: deterministicPrivateKey(t)}, zap.NewNop()).Run(ctx))
	require.Empty(t, publisher.events)
}

func TestRunnerFailsWhenPublishOutcomeIsNotAcceptedOrDuplicate(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewInMemoryNostrEventRepository()
	_, err := repo.Record(ctx, legacyRecord(t, "legacy-1", kinds.WorkerStatus, time.Unix(100, 0).UTC()))
	require.NoError(t, err)
	publisher := &captureMigrationPublisher{outcomes: []PublishOutcome{{RelayURL: "wss://relay.example", Accepted: false, Reason: "blocked: policy"}}}

	err = NewRunner(repo, publisher, nil, Config{PrivateKey: deterministicPrivateKey(t)}, zap.NewNop()).Run(ctx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no accepted or duplicate")
}

func TestRunnerAcceptsExplicitDuplicatePublishOutcome(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewInMemoryNostrEventRepository()
	legacy := legacyRecord(t, "legacy-1", kinds.WorkerStatus, time.Unix(100, 0).UTC())
	_, err := repo.Record(ctx, legacy)
	require.NoError(t, err)
	publisher := &captureMigrationPublisher{outcomes: []PublishOutcome{{RelayURL: "wss://relay.example", Accepted: false, Reason: "duplicate: already have this event"}}}

	require.NoError(t, NewRunner(repo, publisher, nil, Config{PrivateKey: deterministicPrivateKey(t)}, zap.NewNop()).Run(ctx))
	found, err := repo.FindByTag(ctx, "migrated-from", legacy.ID, []int{CanonicalNIP38OperationalState}, 10)
	require.NoError(t, err)
	require.Len(t, found, 1)
}

func TestRunnerMigratesRelayBackfillUntilEOSE(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewInMemoryNostrEventRepository()
	legacy := signedLegacyEvent(t, kinds.PackagePromotionRequest, time.Unix(100, 0).UTC())
	subscriber := &fakeMigrationSubscriber{events: []*gonostr.Event{legacy}}
	publisher := &captureMigrationPublisher{outcomes: []PublishOutcome{{RelayURL: "wss://relay.example", Accepted: true}}}
	runner := NewRunner(repo, publisher, subscriber, Config{PrivateKey: deterministicPrivateKey(t), RelayBackfill: true}, zap.NewNop())

	require.NoError(t, runner.Run(ctx))
	require.Len(t, subscriber.filters, 1)
	require.Contains(t, subscriber.filters[0][0].Kinds, kinds.PackagePromotionRequest)
	require.Len(t, publisher.events, 1)
	found, err := repo.FindByTag(ctx, "migrated-from", legacy.ID, []int{CanonicalContextVMMessage}, 10)
	require.NoError(t, err)
	require.Len(t, found, 1)
}

func TestRunnerDryRunDoesNotPublishOrRecordCanonicalEvent(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewInMemoryNostrEventRepository()
	legacy := legacyRecord(t, "legacy-1", kinds.BackupRunResult, time.Unix(100, 0).UTC())
	_, err := repo.Record(ctx, legacy)
	require.NoError(t, err)
	publisher := &captureMigrationPublisher{outcomes: []PublishOutcome{{Accepted: true}}}

	require.NoError(t, NewRunner(repo, publisher, nil, Config{PrivateKey: deterministicPrivateKey(t), DryRun: true}, zap.NewNop()).Run(ctx))
	require.Empty(t, publisher.events)
	found, err := repo.FindByTag(ctx, "migrated-from", legacy.ID, []int{CanonicalNIP90Feedback}, 10)
	require.NoError(t, err)
	require.Empty(t, found)
}

func signedLegacyEvent(t *testing.T, kind int, createdAt time.Time) *gonostr.Event {
	t.Helper()
	ev := &gonostr.Event{Kind: kind, CreatedAt: gonostr.Timestamp(createdAt.Unix()), Content: `{"ok":true}`, Tags: gonostr.Tags{{"d", "legacy-d"}}}
	require.NoError(t, ev.Sign(deterministicPrivateKey(t)))
	return ev
}

func legacyRecord(t *testing.T, id string, kind int, createdAt time.Time) *repository.NostrEventRecord {
	t.Helper()
	tags, err := json.Marshal(gonostr.Tags{{"d", "legacy-d"}, {"service", "svc-1"}})
	require.NoError(t, err)
	return &repository.NostrEventRecord{ID: id, Kind: kind, PubKey: "legacy-pubkey", Content: `{"ok":true}`, Tags: tags, Sig: "legacy-sig", CreatedAt: createdAt}
}

func tagValues(tags gonostr.Tags, name string) []string {
	values := []string{}
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name {
			values = append(values, tag[1])
		}
	}
	return values
}

func deterministicPrivateKey(t *testing.T) string {
	t.Helper()
	return "0000000000000000000000000000000000000000000000000000000000000001"
}
