package nostrmigration

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrutil"
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
	pages   [][]*gonostr.Event
}

func (s *fakeMigrationSubscriber) SubscribeAllWithEOSE(_ context.Context, filters []gonostr.Filter) (*nostrAdapter.MergedSubscription, error) {
	s.filters = append(s.filters, filters)
	page := s.events
	if len(s.pages) > 0 {
		index := len(s.filters) - 1
		if index < len(s.pages) {
			page = s.pages[index]
		} else {
			page = nil
		}
	}
	events := make(chan *gonostr.Event)
	eose := make(chan struct{})
	go func() {
		defer close(events)
		defer close(eose)
		for _, ev := range page {
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
	require.Equal(t, gonostr.Kind(CanonicalContextVMMessage), published.Kind)
	require.NotEmpty(t, published.ID.Hex())
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
	tags, err := json.Marshal(gonostr.Tags{{"migrated-from", legacy.ID}, {"migration", migrationID}})
	require.NoError(t, err)
	_, err = repo.Record(ctx, &repository.NostrEventRecord{ID: "canonical-existing", Kind: CanonicalCASCPState, Tags: tags, CreatedAt: time.Unix(101, 0).UTC()})
	require.NoError(t, err)
	publisher := &captureMigrationPublisher{outcomes: []PublishOutcome{{Accepted: true}}}

	require.NoError(t, NewRunner(repo, publisher, nil, Config{PrivateKey: deterministicPrivateKey(t)}, zap.NewNop()).Run(ctx))
	require.Empty(t, publisher.events)
}

func TestRunnerPaginatesAllLocalRecordsAndPersistsCursor(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewInMemoryNostrEventRepository()
	for i := 0; i < 5; i++ {
		_, err := repo.Record(ctx, legacyRecord(t, fmt.Sprintf("legacy-%d", i), kinds.DeployRequest, time.Unix(int64(100+i), 0).UTC()))
		require.NoError(t, err)
	}
	publisher := &captureMigrationPublisher{outcomes: []PublishOutcome{{Accepted: true}}}
	runner := NewRunner(repo, publisher, nil, Config{PrivateKey: deterministicPrivateKey(t), LocalBatchLimit: 2}, zap.NewNop())

	require.NoError(t, runner.Run(ctx))
	require.Len(t, publisher.events, 5)
	cursor, err := repo.GetMigrationCursor(ctx, localCursorName)
	require.NoError(t, err)
	require.NotNil(t, cursor)
	require.Equal(t, "legacy-4", cursor.EventID)

	secondPublisher := &captureMigrationPublisher{outcomes: []PublishOutcome{{Accepted: true}}}
	require.NoError(t, NewRunner(repo, secondPublisher, nil, Config{PrivateKey: deterministicPrivateKey(t), LocalBatchLimit: 2}, zap.NewNop()).Run(ctx))
	require.Empty(t, secondPublisher.events)
}

func TestBuildCanonicalEventTranslatesLegacyObjectIntoContextVMParams(t *testing.T) {
	rec := *legacyRecord(t, "legacy-translate", kinds.DeployRequest, time.Unix(100, 0).UTC())
	rec.Content = `{"service_id":"svc-1","replicas":2}`
	disp, ok := ResolveDisposition(rec.Kind, rec.Tags, rec.Content)
	require.True(t, ok)

	ev, err := BuildCanonicalEvent(rec, disp)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(ev.Content), &payload))
	params := payload["params"].(map[string]any)
	require.Equal(t, "svc-1", params["service_id"])
	require.Equal(t, float64(2), params["replicas"])
	require.NotContains(t, params, "content")
	require.Contains(t, tagValues(ev.Tags, "migration"), migrationID)
}

func TestBuildCanonicalEventTranslatesLegacyResultIntoContextVMResponse(t *testing.T) {
	rec := *legacyRecord(t, "legacy-result", kinds.DeploymentResult, time.Unix(100, 0).UTC())
	rec.Content = `{"service_id":"svc-1","ok":true}`
	disp, ok := ResolveDisposition(rec.Kind, rec.Tags, rec.Content)
	require.True(t, ok)

	ev, err := BuildCanonicalEvent(rec, disp)
	require.NoError(t, err)
	require.Equal(t, gonostr.Kind(CanonicalContextVMMessage), ev.Kind)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(ev.Content), &payload))
	require.Equal(t, "2.0", payload["jsonrpc"])
	require.Equal(t, "migration-"+rec.ID, payload["id"])
	result := payload["result"].(map[string]any)
	require.Equal(t, "svc-1", result["service_id"])
	require.Equal(t, true, result["ok"])
}

func TestRunnerRegeneratesLegacyV1MigrationOutput(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewInMemoryNostrEventRepository()
	legacy := legacyRecord(t, "legacy-v1", kinds.DeployRequest, time.Unix(100, 0).UTC())
	_, err := repo.Record(ctx, legacy)
	require.NoError(t, err)
	tags, err := json.Marshal(gonostr.Tags{{"migrated-from", legacy.ID}, {"migration", "bahia-nostr-native-v1"}})
	require.NoError(t, err)
	_, err = repo.Record(ctx, &repository.NostrEventRecord{ID: "bad-v1", Kind: CanonicalContextVMMessage, Tags: tags, CreatedAt: time.Unix(101, 0).UTC()})
	require.NoError(t, err)
	publisher := &captureMigrationPublisher{outcomes: []PublishOutcome{{Accepted: true}}}

	require.NoError(t, NewRunner(repo, publisher, nil, Config{PrivateKey: deterministicPrivateKey(t)}, zap.NewNop()).Run(ctx))
	require.Len(t, publisher.events, 1)
	require.Contains(t, tagValues(publisher.events[0].Tags, "migration"), migrationID)
}

func TestRunnerPaginatesRelayBackfillWindows(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewInMemoryNostrEventRepository()
	newest := signedLegacyEvent(t, kinds.PackagePromotionRequest, time.Unix(300, 0).UTC())
	middle := signedLegacyEvent(t, kinds.PackagePromotionRequest, time.Unix(200, 0).UTC())
	oldest := signedLegacyEvent(t, kinds.PackagePromotionRequest, time.Unix(100, 0).UTC())
	subscriber := &fakeMigrationSubscriber{pages: [][]*gonostr.Event{{newest, middle}, {oldest}}}
	publisher := &captureMigrationPublisher{outcomes: []PublishOutcome{{Accepted: true}}}
	runner := NewRunner(repo, publisher, subscriber, Config{PrivateKey: deterministicPrivateKey(t), RelayBackfill: true, BackfillLimit: 2}, zap.NewNop())

	require.NoError(t, runner.Run(ctx))
	require.Len(t, subscriber.filters, 2)
	require.NotZero(t, subscriber.filters[1][0].Until)
	require.Len(t, publisher.events, 3)
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
	require.Contains(t, subscriber.filters[0][0].Kinds, gonostr.Kind(kinds.PackagePromotionRequest))
	require.Len(t, publisher.events, 1)
	found, err := repo.FindByTag(ctx, "migrated-from", nostrutil.EventIDHex(legacy), []int{CanonicalContextVMMessage}, 10)
	require.NoError(t, err)
	require.Len(t, found, 1)
}

func TestRunnerSkipsInvalidRelayBackfillEventBeforeRecording(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewInMemoryNostrEventRepository()
	legacy := signedLegacyEvent(t, kinds.PackagePromotionRequest, time.Unix(100, 0).UTC())
	legacy.Content = `{"tampered":true}`
	subscriber := &fakeMigrationSubscriber{events: []*gonostr.Event{legacy}}
	publisher := &captureMigrationPublisher{outcomes: []PublishOutcome{{RelayURL: "wss://relay.example", Accepted: true}}}
	runner := NewRunner(repo, publisher, subscriber, Config{PrivateKey: deterministicPrivateKey(t), RelayBackfill: true}, zap.NewNop())

	require.NoError(t, runner.Run(ctx))
	require.Empty(t, publisher.events)
	records, listErr := repo.ListByKinds(ctx, []int{kinds.PackagePromotionRequest}, 10)
	require.NoError(t, listErr)
	require.Empty(t, records)
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
	found, err := repo.FindByTag(ctx, "migrated-from", legacy.ID, []int{CanonicalContextVMMessage}, 10)
	require.NoError(t, err)
	require.Empty(t, found)
}

func signedLegacyEvent(t *testing.T, kind int, createdAt time.Time) *gonostr.Event {
	t.Helper()
	ev := &gonostr.Event{Kind: gonostr.Kind(kind), CreatedAt: gonostr.Timestamp(createdAt.Unix()), Content: `{"ok":true}`, Tags: gonostr.Tags{{"d", "legacy-d"}}}
	require.NoError(t, nostrutil.SignEventWithHexKey(ev, deterministicPrivateKey(t)))
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
