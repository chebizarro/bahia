package nostr

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// These tests wire the Projector to the real control-plane outbox Publisher
// over an in-memory outbox; only the relay transport is faked, per relay.

const (
	cpRelayA = "wss://cp-a.example"
	cpRelayB = "wss://cp-b.example"
)

// relayScript answers EVENT per relay. A relay listed in down answers with a
// transport error; a relay listed in reject answers OK=false with that reason;
// every other relay accepts. accepted is closed the first time acceptFrom
// accepts an event.
type relayScript struct {
	mu         sync.Mutex
	down       map[string]bool
	reject     map[string]string
	calls      map[string][]string // relay -> event ids sent, in order
	acceptFrom string
	accepted   chan struct{}
}

func newRelayScript() *relayScript {
	return &relayScript{down: map[string]bool{}, reject: map[string]string{}, calls: map[string][]string{}, accepted: make(chan struct{})}
}

func (s *relayScript) publish(_ context.Context, ev gonostr.Event, relays []string) ([]PublishResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	results := make([]PublishResult, 0, len(relays))
	for _, url := range relays {
		s.calls[url] = append(s.calls[url], ev.ID.Hex())
		switch {
		case s.down[url]:
			results = append(results, PublishResult{RelayURL: url, Error: errors.New("dial tcp: connection refused")})
		case s.reject[url] != "":
			results = append(results, PublishResult{RelayURL: url, Reason: s.reject[url]})
		default:
			results = append(results, PublishResult{RelayURL: url, Accepted: true})
			if url == s.acceptFrom {
				select {
				case <-s.accepted:
				default:
					close(s.accepted)
				}
			}
		}
	}
	return results, nil
}

func (s *relayScript) setDown(url string, down bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.down[url] = down
}

func (s *relayScript) sent(url string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls[url]...)
}

func (s *relayScript) totalCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, ids := range s.calls {
		n += len(ids)
	}
	return n
}

// newOutboxProjector builds a Projector publishing through a control-plane
// outbox Publisher, wired like app.go (abandon hook included).
func newOutboxProjector(t *testing.T, repo *repository.InMemoryNostrEventRepository, script *relayScript, source *fakeProjectionSource, opts ...ProjectorOption) (*Projector, *Publisher) {
	t.Helper()
	publisher := NewPublisher(projectorTestConfig(), NewRelayPool(nil, zap.NewNop()), repo, zap.NewNop(),
		WithPublishTarget(repository.NostrPublishTargetControlPlane))
	publisher.publishFn = script.publish
	publisher.relayURLs = func() []string { return []string{cpRelayA, cpRelayB} }
	publisher.newBackoff = func() *Backoff {
		return &Backoff{Initial: time.Millisecond, Max: time.Millisecond, Multiplier: 1}
	}
	publisher.idleInterval = time.Millisecond
	projector := NewProjector(projectorTestConfig(), source, publisher, repo, zap.NewNop(), opts...)
	publisher.OnDeliveryAbandoned(projector.ForgetAbandonedProjection)
	return projector, publisher
}

func outboxRows(t *testing.T, repo *repository.InMemoryNostrEventRepository, kind int) []repository.NostrEventRecord {
	t.Helper()
	rows, err := repo.ListByKind(context.Background(), kind, 1000)
	require.NoError(t, err)
	return rows
}

// A projection published while one control-plane relay is down succeeds on
// the other relay's OK, is recorded as a control-plane outbox row, and the
// down relay is retried with the same signed event until it accepts.
func TestProjectorPublishRetriesDownControlPlaneRelayViaOutbox(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewInMemoryNostrEventRepository()
	script := newRelayScript()
	script.setDown(cpRelayB, true)
	script.acceptFrom = cpRelayB
	projector, publisher := newOutboxProjector(t, repo, script, newFakeProjectionSource())

	runCtx, cancel := context.WithCancel(ctx)
	runDone := make(chan error, 1)
	discovered := signalRunnerDiscovery(publisher)
	go func() { runDone <- publisher.Run(runCtx) }()
	// Only an active runner keeps a partially delivered event in memory, so
	// publish once it is running rather than racing its start.
	receive(t, discovered, "first outbox discovery pass")
	defer func() {
		cancel()
		<-runDone
	}()

	serviceID, envID := uuid.New(), uuid.New()
	state := dedupeTestState(serviceID, envID, time.Now().UTC())
	require.NoError(t, projector.publishState(ctx, &state))

	rows := outboxRows(t, repo, KindCASControlState)
	require.Len(t, rows, 1)
	row := rows[0]
	require.Equal(t, repository.NostrPublishTargetControlPlane, row.PublishTarget)
	require.Equal(t, repository.NostrPublishStatePending, row.PublishState, "relay B has not accepted yet")
	require.NotNil(t, row.EntityID, "the projected entity is recorded on the outbox row")
	require.Equal(t, int64(1), projector.ProjectionMetrics()["service/state"].Accepted)

	// Relay B comes back; the outbox runner redelivers to it.
	script.setDown(cpRelayB, false)
	publisher.nudge()
	select {
	case <-script.accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("relay B never received the outbox retry")
	}
	cancel()
	require.NoError(t, <-runDone)
	runDone <- nil // satisfy the deferred receive

	require.Equal(t, []string{row.ID}, script.sent(cpRelayA), "relay A accepted once and is not retried")
	sentB := script.sent(cpRelayB)
	require.GreaterOrEqual(t, len(sentB), 2)
	for _, id := range sentB {
		require.Equal(t, row.ID, id, "relay B is retried with the same signed event")
	}
	stored, err := repo.GetByID(ctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, repository.NostrPublishStatePublished, stored.PublishState)
	require.Len(t, outboxRows(t, repo, KindCASControlState), 1, "no re-signed copy")
}

// A publish no relay accepts yet is queued: the projector reports success,
// counts it as queued, and does not re-sign it on the next trigger.
func TestProjectorTreatsIncompletePublishAsQueued(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewInMemoryNostrEventRepository()
	script := newRelayScript()
	script.setDown(cpRelayA, true)
	script.setDown(cpRelayB, true)
	projector, _ := newOutboxProjector(t, repo, script, newFakeProjectionSource())

	state := dedupeTestState(uuid.New(), uuid.New(), time.Now().UTC())
	require.NoError(t, projector.publishState(ctx, &state))
	require.NoError(t, projector.publishState(ctx, &state))

	m := projector.ProjectionMetrics()["service/state"]
	require.Equal(t, int64(1), m.Queued)
	require.Equal(t, int64(0), m.Rejected)
	require.Equal(t, int64(1), m.Deduped, "the queued event is not re-signed")
	rows := outboxRows(t, repo, KindCASControlState)
	require.Len(t, rows, 1)
	require.Equal(t, repository.NostrPublishStatePending, rows[0].PublishState)
	require.Equal(t, 2, script.totalCalls(), "one delivery round, one EVENT per relay")
}

type staticSBOMProjectionSource struct{ manifests []domain.SBOMManifest }

func (s staticSBOMProjectionSource) ListPublishedManifests(context.Context, int) ([]domain.SBOMManifest, error) {
	return s.manifests, nil
}

func testSBOMManifest() domain.SBOMManifest {
	payload := strings.Repeat("ab", 32)
	return domain.SBOMManifest{
		ID:            uuid.New(),
		Subject:       domain.SBOMSubject{Type: domain.SBOMSubjectArtifact, ID: "artifact-1", DisplayName: "demo", Digest: "sha256:" + strings.Repeat("cd", 32)},
		Format:        domain.SBOMFormatSPDX,
		StorageType:   domain.SBOMStorageBlossom,
		StorageURI:    "https://blossom.example/" + payload,
		PayloadSHA256: payload,
		Generator:     domain.SBOMGenerator{ID: "syft", Version: "1.0.0"},
		ReferenceDTag: "sbom:ref:artifact-1:spdx:" + payload,
		CreatedAt:     time.Unix(1_790_000_000, 0).UTC(),
	}
}

// The periodic repair republishes the whole snapshot. With unchanged content it
// must add no outbox rows and sign nothing, including the SBOM legs and across
// a restart that hydrates from the outbox.
func TestProjectorSnapshotRepairUnchangedCreatesNoRowsOrSignatures(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewInMemoryNostrEventRepository()
	script := newRelayScript()
	source := newFakeProjectionSource()
	svc := domain.Service{ID: uuid.New(), Name: "api"}
	env := domain.Environment{ID: uuid.New(), Name: "prod"}
	source.services[svc.ID] = svc
	source.envs[env.ID] = env
	source.states[stateKeyForTest(svc.ID, env.ID)] = dedupeTestState(svc.ID, env.ID, time.Now().UTC())
	sbomSource := WithSBOMProjectionSource(staticSBOMProjectionSource{manifests: []domain.SBOMManifest{testSBOMManifest()}})

	projector, _ := newOutboxProjector(t, repo, script, source, sbomSource)
	require.NoError(t, projector.RepublishSnapshot(ctx))
	rowsAfterFirst := outboxRowCount(t, repo)
	callsAfterFirst := script.totalCalls()
	require.NotZero(t, rowsAfterFirst)
	require.Len(t, outboxRows(t, repo, 30078), 1, "SBOM reference projected")
	require.Len(t, outboxRows(t, repo, 30004), 1, "SBOM availability list projected")

	require.NoError(t, projector.RepublishSnapshot(ctx))
	require.Equal(t, rowsAfterFirst, outboxRowCount(t, repo), "repair of unchanged content added outbox rows")
	require.Equal(t, callsAfterFirst, script.totalCalls(), "repair of unchanged content signed and sent events")

	restarted, _ := newOutboxProjector(t, repo, script, source, sbomSource)
	require.NoError(t, restarted.RepublishSnapshot(ctx))
	require.Equal(t, rowsAfterFirst, outboxRowCount(t, repo), "restart repair of unchanged content added outbox rows")
	require.Equal(t, callsAfterFirst, script.totalCalls(), "restart repair of unchanged content signed and sent events")
}

// An event the outbox abandons (the quorum became unreachable) must not stay
// in the dedupe cache, or repair would never republish that content.
func TestProjectorRepublishesAbandonedProjection(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewInMemoryNostrEventRepository()
	script := newRelayScript()
	script.reject[cpRelayA] = "blocked: not on the allow list"
	script.reject[cpRelayB] = "blocked: not on the allow list"
	projector, _ := newOutboxProjector(t, repo, script, newFakeProjectionSource())
	projector.projection().now = func() time.Time { return time.Unix(1_800_000_000, 0).UTC() }

	state := dedupeTestState(uuid.New(), uuid.New(), time.Now().UTC())
	err := projector.publishState(ctx, &state)
	require.ErrorIs(t, err, ErrPublishAbandoned, "an abandoned publish is not reported as queued")
	rows := outboxRows(t, repo, KindCASControlState)
	require.Len(t, rows, 1)
	require.Equal(t, repository.NostrPublishStateFailed, rows[0].PublishState)

	// Relays are fixed. Once the projector backoff has passed, the same
	// content is published again rather than deduped against the failed row.
	// (Within the same second it is the identical event id, so the failed row
	// itself is redelivered; later it is a newer replacement.)
	delete(script.reject, cpRelayA)
	delete(script.reject, cpRelayB)
	projector.projection().now = func() time.Time { return time.Unix(1_800_000_000, 0).UTC().Add(projectionBackoffMax * 2) }
	callsBefore := script.totalCalls()
	require.NoError(t, projector.publishState(ctx, &state))
	require.Equal(t, callsBefore+2, script.totalCalls(), "content re-sent to both relays")
	requireLatestPublished(t, repo)
	rowsAfterRepair := len(outboxRows(t, repo, KindCASControlState))

	// A restarted projector hydrates the delivered row and dedupes.
	restarted, _ := newOutboxProjector(t, repo, script, newFakeProjectionSource())
	require.NoError(t, restarted.publishState(ctx, &state))
	require.Len(t, outboxRows(t, repo, KindCASControlState), rowsAfterRepair)
	require.Equal(t, callsBefore+2, script.totalCalls())
}

// requireLatestPublished asserts the newest control-state row was delivered.
func requireLatestPublished(t *testing.T, repo *repository.InMemoryNostrEventRepository) {
	t.Helper()
	rows := outboxRows(t, repo, KindCASControlState)
	require.NotEmpty(t, rows)
	latest := rows[0]
	for _, row := range rows[1:] {
		if row.CreatedAt.After(latest.CreatedAt) {
			latest = row
		}
	}
	require.Equal(t, repository.NostrPublishStatePublished, latest.PublishState)
}

// Delivery abandoned later by the outbox runner (not in the inline round)
// drops the coordinate from the dedupe cache through the abandon hook.
func TestProjectorForgetsProjectionAbandonedByRunner(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewInMemoryNostrEventRepository()
	script := newRelayScript()
	script.setDown(cpRelayA, true)
	script.setDown(cpRelayB, true)
	projector, publisher := newOutboxProjector(t, repo, script, newFakeProjectionSource())

	state := dedupeTestState(uuid.New(), uuid.New(), time.Now().UTC())
	require.NoError(t, projector.publishState(ctx, &state), "queued")
	require.NoError(t, projector.publishState(ctx, &state))
	require.Equal(t, 2, script.totalCalls(), "still deduped while queued")

	// The runner's next round meets permanent rejections and abandons it.
	script.setDown(cpRelayA, false)
	script.setDown(cpRelayB, false)
	script.reject[cpRelayA] = "blocked: gone"
	script.reject[cpRelayB] = "blocked: gone"
	more, err := publisher.discoverPending(ctx)
	require.NoError(t, err)
	require.False(t, more)
	rows := outboxRows(t, repo, KindCASControlState)
	require.Len(t, rows, 1)
	require.Equal(t, repository.NostrPublishStateFailed, rows[0].PublishState)

	delete(script.reject, cpRelayA)
	delete(script.reject, cpRelayB)
	callsBefore := script.totalCalls()
	require.NoError(t, projector.publishState(ctx, &state))
	require.Equal(t, callsBefore+2, script.totalCalls(), "abandoned content is published again")
	requireLatestPublished(t, repo)
}

func outboxRowCount(t *testing.T, repo *repository.InMemoryNostrEventRepository) int {
	t.Helper()
	rows, err := repo.FindSince(context.Background(), time.Time{}, nil)
	require.NoError(t, err)
	return len(rows)
}

// A restarted projector must not hydrate an abandoned (failed) row as
// published content, or the coordinate would never be repaired.
func TestProjectorHydrationSkipsFailedOutboxRows(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewInMemoryNostrEventRepository()
	script := newRelayScript()
	script.reject[cpRelayA] = "blocked: nope"
	script.reject[cpRelayB] = "blocked: nope"
	first, _ := newOutboxProjector(t, repo, script, newFakeProjectionSource())
	state := dedupeTestState(uuid.New(), uuid.New(), time.Now().UTC())
	require.ErrorIs(t, first.publishState(ctx, &state), ErrPublishAbandoned)
	rows := outboxRows(t, repo, KindCASControlState)
	require.Len(t, rows, 1)
	require.Equal(t, repository.NostrPublishStateFailed, rows[0].PublishState)

	delete(script.reject, cpRelayA)
	delete(script.reject, cpRelayB)
	callsBefore := script.totalCalls()
	restarted, _ := newOutboxProjector(t, repo, script, newFakeProjectionSource())
	require.NoError(t, restarted.publishState(ctx, &state))
	require.Equal(t, callsBefore+2, script.totalCalls(), "failed row did not suppress the publish")
	requireLatestPublished(t, repo)
}
