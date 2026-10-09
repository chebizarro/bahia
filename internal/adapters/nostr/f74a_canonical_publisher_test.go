package nostr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/repository/repositorytest"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestF74aCanonicalFamiliesPublishAndTombstone(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repositorytest.NewInMemoryNostrEventRepository(), zap.NewNop())
	enc := &mockConfidentialEncryptor{}
	pub := NewF74aCanonicalPublisher(projector, enc)
	release := &domain.LLMRelease{ID: uuid.New(), RouteID: uuid.New(), Version: "v1", CreatedAt: time.Now().UTC()}
	sig := &domain.ArtifactSignature{ID: uuid.New(), ArtifactID: uuid.New(), SignerIdentity: "builder@example.test", CreatedAt: time.Now().UTC()}
	sbom := &domain.ArtifactSBOM{ID: uuid.New(), ArtifactID: sig.ArtifactID, Format: domain.SBOMFormatSPDX, CreatedAt: time.Now().UTC()}
	pkg := &domain.SBOMPackage{ID: uuid.New(), SBOMID: sbom.ID, Name: "module"}
	obs := &domain.RuntimeObservation{ID: uuid.New(), ServiceID: uuid.New(), EnvironmentID: uuid.New(), ObservedAt: time.Now().UTC(), Metadata: map[string]any{"password": "do-not-project"}}
	cases := []struct {
		kind               int
		topic, d           string
		publish, tombstone func() error
	}{
		{KindLLMReleaseRegistry, kinds.CPStateTopicLLMRelease, "llm:release:" + release.ID.String(), func() error { return pub.PublishLLMRelease(ctx, release) }, func() error { return pub.PublishLLMReleaseState(ctx, release, true) }},
		{KindArtifactSignatureRegistry, kinds.CPStateTopicArtifactSignature, "artifact:signature:" + sig.ID.String(), func() error { return pub.PublishArtifactSignature(ctx, sig) }, func() error { return pub.PublishArtifactSignatureState(ctx, sig, true) }},
		{KindArtifactSBOMRegistry, kinds.CPStateTopicArtifactSBOM, "artifact:sbom:" + sbom.ID.String(), func() error { return pub.PublishArtifactSBOM(ctx, sbom) }, func() error { return pub.PublishArtifactSBOMState(ctx, sbom, true) }},
		{KindSBOMPackageRegistry, kinds.CPStateTopicSBOMPackage, SBOMPackageDTag(pkg), func() error { return pub.PublishSBOMPackage(ctx, pkg) }, func() error { return pub.PublishSBOMPackageState(ctx, pkg, true) }},
		{KindRuntimeObservationState, kinds.CPStateTopicRuntimeObservation, "runtime:observation:" + obs.ServiceID.String() + ":" + obs.EnvironmentID.String(), func() error { return pub.PublishRuntimeObservation(ctx, obs) }, func() error { return pub.PublishRuntimeObservationState(ctx, obs, true) }},
	}
	for _, tc := range cases {
		t.Run(strconv.Itoa(tc.kind), func(t *testing.T) {
			before := len(sink.byKind(KindCASControlState))
			if err := tc.publish(); err != nil {
				t.Fatal(err)
			}
			if err := tc.tombstone(); err != nil {
				t.Fatal(err)
			}
			events := sink.byKind(KindCASControlState)
			if len(events) != before+2 {
				t.Fatalf("got %d new records, want 2", len(events)-before)
			}
			live, dead := events[before], events[before+1]
			for _, ev := range []struct {
				tags        gonostr.Tags
				wantDeleted string
			}{{live.Tags, "false"}, {dead.Tags, "true"}} {
				if !hasTag(ev.tags, "d", tc.d) || !hasTag(ev.tags, "t", tc.topic) || !hasTag(ev.tags, "legacy_kind", strconv.Itoa(tc.kind)) || !hasTag(ev.tags, "deleted", ev.wantDeleted) {
					t.Fatalf("incorrect canonical envelope: %v", ev.tags)
				}
			}
			if live.Kind != KindCASControlState || dead.Kind != KindCASControlState {
				t.Fatal("not 30900")
			}
			if len(live.Content) > f74aMaxContent || len(dead.Content) > f74aMaxContent {
				t.Fatal("oversized event content")
			}
			if tc.kind == KindRuntimeObservationState && strings.Contains(live.Content, "do-not-project") {
				t.Fatal("runtime secret metadata leaked")
			}
		})
	}
	if enc.lastLegacyKind != KindLLMReleaseRegistry || enc.lastTopic != kinds.CPStateTopicLLMRelease {
		t.Fatal("LLM release was not confidential")
	}
}

func TestF74aPackageIndexAndSizeBounds(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repositorytest.NewInMemoryNostrEventRepository(), zap.NewNop())
	pub := NewF74aCanonicalPublisher(projector, &mockConfidentialEncryptor{})
	sbomID := uuid.New()
	for i := 0; i < 64; i++ {
		pkg := &domain.SBOMPackage{ID: uuid.New(), SBOMID: sbomID, Name: fmt.Sprintf("pkg-%d", i)}
		if err := pub.PublishSBOMPackage(ctx, pkg); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(sink.byKind(KindCASControlState)); got != 64 {
		t.Fatalf("package index has %d events, want 64", got)
	}
	huge := &domain.SBOMPackage{ID: uuid.New(), SBOMID: sbomID, Name: strings.Repeat("x", f74aMaxContent)}
	if err := pub.PublishSBOMPackage(ctx, huge); err == nil {
		t.Fatal("oversized package accepted")
	}
	release := &domain.LLMRelease{ID: uuid.New(), RouteID: uuid.New(), Metadata: map[string]any{"blob": strings.Repeat("x", f74aMaxConfidentialPlaintext)}}
	if err := pub.PublishLLMRelease(ctx, release); err == nil {
		t.Fatal("oversized confidential release accepted")
	}
	if got := len(sink.byKind(KindCASControlState)); got != 64 {
		t.Fatalf("size rejection published %d events", got-64)
	}
}

type f74aDirtyMarker struct {
	value  string
	writes int
}

func (m *f74aDirtyMarker) PutControlRecord(_, _ string, value []byte) error {
	m.value = string(value)
	m.writes++
	return nil
}
func TestF74aPublishFailureSchedulesStartupRepair(t *testing.T) {
	sink := &captureProjectionPublisher{}
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repositorytest.NewInMemoryNostrEventRepository(), zap.NewNop())
	marker := &f74aDirtyMarker{}
	pub := NewF74aCanonicalPublisher(projector, nil, marker)
	pkg := &domain.SBOMPackage{ID: uuid.New(), SBOMID: uuid.New(), Name: strings.Repeat("x", f74aMaxContent)}
	if err := pub.PublishSBOMPackage(context.Background(), pkg); err == nil {
		t.Fatal("expected frame bound rejection")
	}
	if marker.value != "dirty" || marker.writes != 1 {
		t.Fatalf("repair marker = %q, writes=%d", marker.value, marker.writes)
	}
	if len(sink.byKind(KindCASControlState)) != 0 {
		t.Fatal("oversized state entered outbox")
	}
}

func TestSBOMPackageDTagUsesExactSemanticTuple(t *testing.T) {
	sbomID := uuid.MustParse("00112233-4455-6677-8899-aabbccddeeff")
	pkg := &domain.SBOMPackage{ID: uuid.New(), SBOMID: sbomID, Name: "a\x00b", Version: "1", Ecosystem: "npm", PURL: "pkg:npm/a@1"}
	const want = "artifact:sbom-package:v2:a5384b95ec21d7455455180e1954a98b05c5099dcb484a84773e6fa48431877b"
	require.Equal(t, want, SBOMPackageDTag(pkg))
	copy := *pkg
	copy.ID = uuid.New()
	require.Equal(t, want, SBOMPackageDTag(&copy), "database row identity must not affect the coordinate")
	changes := []func(*domain.SBOMPackage){
		func(p *domain.SBOMPackage) { p.SBOMID = uuid.New() },
		func(p *domain.SBOMPackage) { p.Name = "a" },
		func(p *domain.SBOMPackage) { p.Version = "2" },
		func(p *domain.SBOMPackage) { p.Ecosystem = "go" },
		func(p *domain.SBOMPackage) { p.License = "MIT" },
		func(p *domain.SBOMPackage) { p.PURL = "pkg:npm/b@1" },
		func(p *domain.SBOMPackage) { p.CPE = "cpe:/a:test" },
	}
	for _, change := range changes {
		copy = *pkg
		change(&copy)
		require.NotEqual(t, want, SBOMPackageDTag(&copy))
	}
	copy = *pkg
	copy.Name, copy.Version = "a", "b\x001"
	require.NotEqual(t, want, SBOMPackageDTag(&copy), "field boundaries must be unambiguous")
}

func TestF74aLegacyPackageTombstoneUsesOldCoordinate(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repositorytest.NewInMemoryNostrEventRepository(), zap.NewNop())
	pub := NewF74aCanonicalPublisher(projector, nil)
	pkg := &domain.SBOMPackage{ID: uuid.New(), SBOMID: uuid.New(), Name: "module", Version: "1"}
	require.NoError(t, pub.PublishSBOMPackage(ctx, pkg))
	require.NoError(t, pub.PublishLegacySBOMPackageTombstone(ctx, pkg))
	all := sink.byKind(KindCASControlState)
	require.Len(t, all, 2)
	require.True(t, hasTag(all[0].Tags, "d", SBOMPackageDTag(pkg)))
	require.True(t, hasTag(all[0].Tags, "deleted", "false"))
	require.True(t, hasTag(all[1].Tags, "d", "artifact:sbom-package:"+pkg.ID.String()))
	require.True(t, hasTag(all[1].Tags, "deleted", "true"))
	require.Equal(t, all[0].PubKey, all[1].PubKey)
	require.Equal(t, all[0].Kind, all[1].Kind)
	require.NotEqual(t, eventDTag(all[0]), eventDTag(all[1]))
}

// A kind-wide hydration result can omit an older pending package coordinate
// once the daemon holds more than projectionHydrateLimit other 30900 records.
// The direct coordinate read must be used instead of that capped result.
type packageCoordinateOnlyHistory struct {
	ProjectionHistory
	coordinate ProjectionCoordinateHistory
	listCalls  int
}

func (h *packageCoordinateOnlyHistory) ListByKind(context.Context, int, int) ([]repository.NostrEventRecord, error) {
	h.listCalls++
	return nil, errors.New("kind-wide history is outside the bounded package replay path")
}

func (h *packageCoordinateOnlyHistory) LatestByCoordinate(ctx context.Context, kind int, d string) (*repository.NostrEventRecord, error) {
	return h.coordinate.LatestByCoordinate(ctx, kind, d)
}

func TestF74aQueuedSemanticPackageAndLegacyTombstoneDedupeAcrossRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	script := newRelayScript()
	script.setDown(cpRelayA, true)
	script.setDown(cpRelayB, true)
	pkg := &domain.SBOMPackage{ID: uuid.New(), SBOMID: uuid.New(), Name: "module", Version: "1"}

	first := startLocalHistoryDaemon(t, dir, script)
	firstPub := NewF74aCanonicalPublisher(first.projector, nil)
	require.NoError(t, firstPub.PublishSBOMPackage(ctx, pkg))
	require.NoError(t, firstPub.PublishSBOMPackage(ctx, pkg))
	counts, err := first.outbox.Counts()
	require.NoError(t, err)
	require.Equal(t, int64(1), counts.Pending)
	require.NoError(t, firstPub.PublishLegacySBOMPackageTombstone(ctx, pkg))
	require.NoError(t, firstPub.PublishLegacySBOMPackageTombstone(ctx, pkg))
	counts, err = first.outbox.Counts()
	require.NoError(t, err)
	require.Equal(t, int64(2), counts.Pending)
	for ev := range first.store.QueryEvents(gonostr.Filter{Kinds: []gonostr.Kind{KindCASControlState}, Tags: gonostr.TagMap{"d": {"artifact:sbom-package:" + pkg.ID.String()}}, Limit: 1}) {
		require.NoError(t, first.store.DeleteEvent(ev.ID), "the retained tombstone must survive a missing event-store cache record")
	}
	first.close()

	restarted := startLocalHistoryDaemon(t, dir, script)
	history := &packageCoordinateOnlyHistory{
		ProjectionHistory: restarted.projector.history,
		coordinate:        restarted.projector.history.(ProjectionCoordinateHistory),
	}
	restarted.projector.history = history
	restartedPub := NewF74aCanonicalPublisher(restarted.projector, nil)
	require.NoError(t, restartedPub.PublishSBOMPackage(ctx, pkg))
	require.NoError(t, restartedPub.PublishLegacySBOMPackageTombstone(ctx, pkg))
	counts, err = restarted.outbox.Counts()
	require.NoError(t, err)
	require.Equal(t, int64(2), counts.Pending, "cursor replay must not add signed pending events")
	require.Zero(t, history.listCalls, "package replay must not read a capped kind-wide horizon")
	require.Equal(t, 4, script.totalCalls(), "each distinct coordinate was attempted against two relays once")
	pending, err := restarted.outbox.ListPending("control-plane", nil, 10)
	require.NoError(t, err)
	require.Len(t, pending, 2)
	for _, entry := range pending {
		require.Equal(t, localstore.OutboxPending, entry.State)
	}
}

func TestF74aSemanticPackageChangedRepresentativeReplacesWithNewerEvent(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repositorytest.NewInMemoryNostrEventRepository(), zap.NewNop())
	pub := NewF74aCanonicalPublisher(projector, nil)
	pkg := &domain.SBOMPackage{ID: uuid.New(), SBOMID: uuid.New(), Name: "module", Version: "1"}
	require.NoError(t, pub.PublishSBOMPackage(ctx, pkg))
	otherRow := *pkg
	otherRow.ID = uuid.New()
	require.NoError(t, pub.PublishSBOMPackage(ctx, &otherRow))
	events := sink.byKind(KindCASControlState)
	require.Len(t, events, 2)
	require.Equal(t, eventDTag(events[0]), eventDTag(events[1]))
	require.Greater(t, events[1].CreatedAt, events[0].CreatedAt)
	var content domain.SBOMPackage
	require.NoError(t, json.Unmarshal([]byte(events[1].Content), &content))
	require.Equal(t, otherRow.ID, content.ID, "representative row identity remains visible in content")
	require.NoError(t, pub.PublishSBOMPackage(ctx, &otherRow))
	require.Len(t, sink.byKind(KindCASControlState), 2, "a live import normalized to the same representative does not re-sign")
}

func TestF74aLegacyTombstoneRefusalIsNotReportedAsStaged(t *testing.T) {
	ctx := context.Background()
	repo := repositorytest.NewInMemoryNostrEventRepository()
	script := newRelayScript()
	projector, _ := newOutboxProjector(t, repo, script, newFakeProjectionSource())
	marker := &f74aDirtyMarker{}
	pub := NewF74aCanonicalPublisher(projector, nil, marker)
	pkg := &domain.SBOMPackage{ID: uuid.New(), SBOMID: uuid.New(), Name: "module"}
	require.NoError(t, pub.PublishSBOMPackage(ctx, pkg))
	script.mu.Lock()
	script.reject[cpRelayA] = "blocked: legacy cleanup rejected"
	script.reject[cpRelayB] = "blocked: legacy cleanup rejected"
	script.mu.Unlock()
	err := pub.PublishLegacySBOMPackageTombstone(ctx, pkg)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrPublishAbandoned))
	require.Equal(t, "dirty", marker.value)
	require.Equal(t, 1, marker.writes)
}

func TestF74aPendingPackageSurvivesCrashBeforeEventStoreCacheWrite(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	script := newRelayScript()
	script.setDown(cpRelayA, true)
	script.setDown(cpRelayB, true)
	pkg := &domain.SBOMPackage{ID: uuid.New(), SBOMID: uuid.New(), Name: "module"}
	first := startLocalHistoryDaemon(t, dir, script)
	require.NoError(t, NewF74aCanonicalPublisher(first.projector, nil).PublishSBOMPackage(ctx, pkg))
	var held gonostr.Event
	for ev := range first.store.QueryEvents(gonostr.Filter{Kinds: []gonostr.Kind{KindCASControlState}, Tags: gonostr.TagMap{"d": {SBOMPackageDTag(pkg)}}, Limit: 1}) {
		held = ev
	}
	require.NotEqual(t, gonostr.ZeroID, held.ID)
	require.NoError(t, first.store.DeleteEvent(held.ID), "simulate crash after outbox commit and before cache write")
	first.close()

	restarted := startLocalHistoryDaemon(t, dir, script)
	require.NoError(t, NewF74aCanonicalPublisher(restarted.projector, nil).PublishSBOMPackage(ctx, pkg))
	counts, err := restarted.outbox.Counts()
	require.NoError(t, err)
	require.Equal(t, int64(1), counts.Pending)
	require.Equal(t, 2, script.totalCalls(), "restart must not sign or send a second event")
}

func TestF74aPackageDedupeAfterPublishedOutboxEntryPruned(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	script := newRelayScript()
	pkg := &domain.SBOMPackage{ID: uuid.New(), SBOMID: uuid.New(), Name: "module"}
	first := startLocalHistoryDaemon(t, dir, script)
	require.NoError(t, NewF74aCanonicalPublisher(first.projector, nil).PublishSBOMPackage(ctx, pkg))
	require.Equal(t, 2, script.totalCalls())
	removed, err := first.outbox.Prune(time.Now().Add(time.Hour), time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, removed, "only settled entries may be pruned")
	first.close()

	restarted := startLocalHistoryDaemon(t, dir, script)
	require.NoError(t, NewF74aCanonicalPublisher(restarted.projector, nil).PublishSBOMPackage(ctx, pkg))
	require.Equal(t, 2, script.totalCalls(), "the retained local event prevents re-signing after outbox pruning")
	counts, err := restarted.outbox.Counts()
	require.NoError(t, err)
	require.Zero(t, counts.Pending)
}

func TestF74aFailedOutboxWinsEqualIDUnmarkedLocalEventAfterCrash(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	script := newRelayScript()
	script.setDown(cpRelayA, true)
	script.setDown(cpRelayB, true)
	pkg := &domain.SBOMPackage{ID: uuid.New(), SBOMID: uuid.New(), Name: "module"}
	first := startLocalHistoryDaemon(t, dir, script)
	require.NoError(t, NewF74aCanonicalPublisher(first.projector, nil).PublishSBOMPackage(ctx, pkg))
	pending, err := first.outbox.ListPending("control-plane", nil, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	original := pending[0].Event
	_, err = first.outbox.CommitRound(original.ID, localstore.OutboxRound{
		State: localstore.OutboxFailed, Detail: "blocked: rejected", At: time.Now(),
	})
	require.NoError(t, err)
	// Simulate a crash before markOwnEventUndelivered writes its marker.
	local, err := first.projector.history.(ProjectionCoordinateHistory).LatestByCoordinate(ctx, KindCASControlState, SBOMPackageDTag(pkg))
	require.NoError(t, err)
	require.NotNil(t, local)
	require.Equal(t, original.ID.Hex(), local.ID)
	require.NotEqual(t, repository.NostrPublishStateFailed, local.PublishState)
	first.close()

	script.setDown(cpRelayA, false)
	script.setDown(cpRelayB, false)
	restarted := startLocalHistoryDaemon(t, dir, script)
	require.NoError(t, NewF74aCanonicalPublisher(restarted.projector, nil).PublishSBOMPackage(ctx, pkg))
	require.Equal(t, 4, script.totalCalls(), "failed delivery must be re-signed and sent after restart")
	latest, err := restarted.projector.history.(ProjectionCoordinateHistory).LatestByCoordinate(ctx, KindCASControlState, SBOMPackageDTag(pkg))
	require.NoError(t, err)
	require.NotNil(t, latest)
	require.NotEqual(t, original.ID.Hex(), latest.ID)
	require.True(t, latest.CreatedAt.After(original.CreatedAt.Time()))
}

func TestF74aMigrationTombstoneBackoffReplayKeepsDedupe(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repositorytest.NewInMemoryNostrEventRepository(), zap.NewNop())
	pub := NewF74aCanonicalPublisher(projector, nil)
	pkg := &domain.SBOMPackage{ID: uuid.New(), SBOMID: uuid.New(), Name: "module"}
	clock := time.Unix(1_800_000_000, 0).UTC()
	state := projector.projection()
	state.now = func() time.Time { return clock }
	state.backoffMu.Lock()
	state.retryAfter = clock.Add(time.Hour)
	state.backoffMu.Unlock()
	require.ErrorIs(t, pub.PublishLegacySBOMPackageTombstone(ctx, pkg), ErrProjectorBackoff)
	state.mu.Lock()
	require.Len(t, state.pendingRetries, 1)
	for _, pending := range state.pendingRetries {
		require.True(t, pending.dedupeTombstone)
	}
	state.mu.Unlock()

	clock = clock.Add(2 * time.Hour)
	require.NoError(t, pub.PublishLegacySBOMPackageTombstone(ctx, pkg), "trigger signs tombstone before saved retry flushes")
	projector.flushPendingRetries()
	require.Len(t, sink.byKind(KindCASControlState), 1, "queued retry must not re-sign the same migration tombstone")
}

func TestNewerProjectionRecordPreservesNIP01OrderButUsesOutboxStateForSameID(t *testing.T) {
	at := time.Unix(1_800_000_000, 0).UTC()
	current := &repository.NostrEventRecord{ID: "b", CreatedAt: at}
	sameFailed := &repository.NostrEventRecord{ID: "b", CreatedAt: at, PublishState: repository.NostrPublishStateFailed}
	require.True(t, newerProjectionRecord(sameFailed, current), "delivery state of same signed event comes from outbox")
	require.False(t, newerProjectionRecord(&repository.NostrEventRecord{ID: "a", CreatedAt: at.Add(-time.Second)}, current))
	require.False(t, newerProjectionRecord(&repository.NostrEventRecord{ID: "b", CreatedAt: at.Add(-time.Second)}, current))
	require.True(t, newerProjectionRecord(&repository.NostrEventRecord{ID: "z", CreatedAt: at.Add(time.Second)}, current))
	require.True(t, newerProjectionRecord(&repository.NostrEventRecord{ID: "a", CreatedAt: at}, current), "lowest ID wins timestamp tie")
}
