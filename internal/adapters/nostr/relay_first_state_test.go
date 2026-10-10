package nostr

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// the relay-first registry and the projector write the same
// service- and environment-registry coordinates. These tests drive the
// production relay-first path (service.RelayFirstRegistry over
// RelayFirstStatePublisher) and the projector over one registry cache.

// relayFirstHarness is one daemon: a registry cache, the projector reading
// it, and the relay-first registry writing through the projector's state.
type relayFirstHarness struct {
	services       *relayFirstTestServiceRepo
	environments   *relayFirstTestEnvironmentRepo
	source         *service.RegistryService
	projector      *Projector
	projectorRelay *captureProjectionPublisher
	relayFirst     *captureProjectionPublisher
	registry       *service.RelayFirstRegistry
}

func newRelayFirstHarness(t *testing.T, stampRevision bool) *relayFirstHarness {
	t.Helper()
	h := &relayFirstHarness{
		services:       &relayFirstTestServiceRepo{rows: map[uuid.UUID]domain.Service{}, stampRevision: stampRevision},
		environments:   &relayFirstTestEnvironmentRepo{rows: map[uuid.UUID]domain.Environment{}},
		projectorRelay: &captureProjectionPublisher{},
		relayFirst:     &captureProjectionPublisher{},
	}
	h.source = service.NewRegistryService(h.services, h.environments, nil, nil, nil, nil, nil, nil, nil, &events.NoopPublisher{}, zap.NewNop())
	h.projector = newRelayFirstTestProjector(h.source, h.projectorRelay)
	h.registry = service.NewRelayFirstRegistry(h.source, NewRelayFirstStatePublisher(h.projector, newRelayFirstTestPublisher(h.relayFirst)), zap.NewNop())
	return h
}

// republishRegistrySnapshot runs the service and environment
// loop (the rest of the snapshot reads repositories this cache does not have).
func (h *relayFirstHarness) republishRegistrySnapshot(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	services, err := h.source.ListServices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range services {
		if err := h.projector.publishServiceRegistry(ctx, &services[i], false); err != nil {
			t.Fatalf("snapshot service %s: %v", services[i].ID, err)
		}
	}
	envs, err := h.source.ListEnvironments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range envs {
		if err := h.projector.publishEnvironmentRegistry(ctx, &envs[i], false); err != nil {
			t.Fatalf("snapshot environment %s: %v", envs[i].ID, err)
		}
	}
}

// newRelayFirstTestPublisher is a control-plane publisher with one write
// relay, sink, and no outbox: PublishBeforeCommit's round goes to sink.
func newRelayFirstTestPublisher(sink *captureProjectionPublisher) *Publisher {
	const relayURL = "wss://relay-first.test"
	publisher := newKeyedTestPublisher(config.NostrConfig{PrivateKey: projectorTestPrivateKey}, NewRelayPool([]string{relayURL}, zap.NewNop()), nil, zap.NewNop())
	publisher.publishFn = func(ctx context.Context, ev gonostr.Event, urls []string) ([]PublishResult, error) {
		accepted, err := sink.Publish(ctx, ev)
		return []PublishResult{{RelayURL: relayURL, Accepted: accepted > 0, Error: err}}, nil
	}
	return publisher
}

func newRelayFirstTestProjector(source ProjectionSource, sink relaySink) *Projector {
	cfg := config.NostrConfig{PrivateKey: projectorTestPrivateKey, PublishEnabled: true}
	return newTestProjector(cfg, source, sink, nil, zap.NewNop())
}

// projectAlone signs the registry record a projector with no shared history
// publishes for the cached row (the bus event's audit fact aside), the
// reference shape both writers must produce.
func projectAlone(t *testing.T, source ProjectionSource, eventType events.EventType, legacyKind int, id uuid.UUID) gonostr.Event {
	t.Helper()
	sink := &captureProjectionPublisher{}
	newRelayFirstTestProjector(source, sink).handleEvent(context.Background(), events.Event{Type: eventType, EntityID: id.String()})
	records := sink.byKind(legacyKind)
	if len(records) != 1 {
		t.Fatalf("reference projection of %s published %d records, want 1", id, len(records))
	}
	return records[0]
}

func relayFirstTestTimes() (created, updated time.Time) {
	created = time.Date(2026, 9, 30, 10, 0, 0, 123456000, time.UTC)
	return created, created.Add(90*time.Minute + 789*time.Microsecond)
}

func TestRelayFirstServiceRecordMatchesProjectionAndIsSignedOnce(t *testing.T) {
	ctx := context.Background()
	h := newRelayFirstHarness(t, false)
	created, updated := relayFirstTestTimes()
	stored := domain.Service{
		ID: domain.NewEntityID(), OrgID: uuid.New(), Name: "payments-api",
		RepoURL: "https://git.example/acme/payments.git", ArtifactRepo: "registry.example/payments",
		DefaultBranch: "main", RuntimeType: domain.RuntimeTypeDocker, CreatedAt: created, UpdatedAt: updated,
	}
	h.services.rows[stored.ID] = stored

	edit := stored
	edit.ArtifactRepo = "registry.example/payments-v2"
	if err := h.registry.UpdateService(ctx, &edit); err != nil {
		t.Fatalf("relay-first UpdateService: %v", err)
	}
	if len(h.relayFirst.events) != 1 {
		t.Fatalf("relay-first published %d events, want 1", len(h.relayFirst.events))
	}
	relayFirst := h.relayFirst.events[0]

	// The projector does not publish service registry records;
	// the relay-first path is the only publisher.
	assertCPStateEnvelope(t, relayFirst, KindServiceRegistry, stored.ID.String(), false, kinds.CPStateTopicServiceRegistry)
	var content map[string]any
	if err := json.Unmarshal([]byte(relayFirst.Content), &content); err != nil {
		t.Fatalf("decode relay-first content: %v", err)
	}
	// The update's revision is minted before publishing:
	// the record carries the revision the cache then stored, newer than the
	// one the edit started from.
	revision := h.services.rows[stored.ID].UpdatedAt
	if !revision.After(updated) {
		t.Fatalf("cached revision %s did not advance past %s", revision, updated)
	}
	if content["org_id"] != stored.OrgID.String() || content["updated_at"] != revision.Format(time.RFC3339Nano) {
		t.Fatalf("relay-first content org_id=%v updated_at=%v, want %s and the cached revision %s",
			content["org_id"], content["updated_at"], stored.OrgID, revision.Format(time.RFC3339Nano))
	}
	assertWebReadModelFilterMatches(t, relayFirst)

	// The projector does not handle service events at all.
	h.projector.handleEvent(ctx, events.Event{Type: events.EventServiceUpdated, EntityID: stored.ID.String()})
	h.republishRegistrySnapshot(t)
	if got := h.projectorRelay.byKind(KindServiceRegistry); len(got) != 0 {
		t.Fatalf("projector published %d service records on service event, want 0", len(got))
	}
}

func TestRelayFirstEnvironmentRecordMatchesProjectionAndIsSignedOnce(t *testing.T) {
	ctx := context.Background()
	h := newRelayFirstHarness(t, false)
	created, updated := relayFirstTestTimes()
	stored := domain.Environment{
		ID: domain.NewEntityID(), OrgID: uuid.New(), Name: "prod",
		LoomWorkerSelector: map[string]any{"region": "eu"}, DeployStrategy: domain.DeployStrategyReplace,
		Targeting: domain.EnvironmentTargeting{FailureDomainLabels: map[string]string{"zone": "a"}},
		CreatedAt: created, UpdatedAt: updated,
	}
	h.environments.rows[stored.ID] = stored

	edit := stored
	edit.Protected = true
	if err := h.registry.UpdateEnvironment(ctx, &edit); err != nil {
		t.Fatalf("relay-first UpdateEnvironment: %v", err)
	}
	if len(h.relayFirst.events) != 1 {
		t.Fatalf("relay-first published %d events, want 1", len(h.relayFirst.events))
	}
	relayFirst := h.relayFirst.events[0]

	// The projector has no environment handleEvent case; use direct
	// publishEnvironmentRegistry for the reference projection.
	refSink := &captureProjectionPublisher{}
	refProj := newRelayFirstTestProjector(h.source, refSink)
	env, _ := h.source.GetEnvironment(ctx, stored.ID)
	if err := refProj.publishEnvironmentRegistry(ctx, env, false); err != nil {
		t.Fatalf("reference publishEnvironmentRegistry: %v", err)
	}
	refRecords := refSink.byKind(KindEnvironmentRegistry)
	if len(refRecords) != 1 {
		t.Fatalf("reference projection of %s published %d records, want 1", stored.ID, len(refRecords))
	}
	want := refRecords[0]
	assertSameRecord(t, relayFirst, want)
	assertCPStateEnvelope(t, relayFirst, KindEnvironmentRegistry, stored.ID.String(), false, kinds.CPStateTopicEnvironmentRegistry)
	var content map[string]any
	if err := json.Unmarshal([]byte(relayFirst.Content), &content); err != nil {
		t.Fatalf("decode relay-first content: %v", err)
	}
	if content["org_id"] != stored.OrgID.String() || content["protected"] != true {
		t.Fatalf("relay-first content org_id=%v protected=%v", content["org_id"], content["protected"])
	}
	if _, ok := content["runtime_config"].(map[string]any); !ok {
		t.Fatalf("runtime_config = %#v, want an object for an unset map", content["runtime_config"])
	}
	assertWebReadModelFilterMatches(t, relayFirst)

	h.projector.handleEvent(ctx, events.Event{Type: events.EventEnvironmentUpdated, EntityID: stored.ID.String()})
	h.republishRegistrySnapshot(t)
	if got := h.projectorRelay.byKind(KindEnvironmentRegistry); len(got) != 0 {
		t.Fatalf("projector re-signed an unchanged relay-first record: %d events", len(got))
	}
}

func TestRelayFirstTombstonesMatchProjectorTombstones(t *testing.T) {
	ctx := context.Background()
	deletedAt := time.Date(2026, 10, 1, 8, 0, 0, 5000, time.UTC)
	serviceID, environmentID := domain.NewEntityID(), domain.NewEntityID()

	relayFirstSink := &captureProjectionPublisher{}
	writer := NewRelayFirstStatePublisher(newRelayFirstTestProjector(nil, nil), newRelayFirstTestPublisher(relayFirstSink))
	if err := writer.PublishServiceRegistry(ctx, &domain.Service{ID: serviceID, UpdatedAt: deletedAt}, true); err != nil {
		t.Fatalf("relay-first service tombstone: %v", err)
	}
	if err := writer.PublishEnvironmentRegistry(ctx, &domain.Environment{ID: environmentID, UpdatedAt: deletedAt}, nil, true); err != nil {
		t.Fatalf("relay-first environment tombstone: %v", err)
	}
	relayFirst := relayFirstSink.events

	sink := &captureProjectionPublisher{}
	projector := newRelayFirstTestProjector(nil, sink)
	if err := projector.publishServiceRegistry(ctx, &domain.Service{ID: serviceID, UpdatedAt: deletedAt}, true); err != nil {
		t.Fatalf("projector service tombstone: %v", err)
	}
	if err := projector.publishEnvironmentRegistry(ctx, &domain.Environment{ID: environmentID, UpdatedAt: deletedAt}, true); err != nil {
		t.Fatalf("projector environment tombstone: %v", err)
	}
	if len(relayFirst) != 2 || len(sink.events) != 2 {
		t.Fatalf("tombstones: relay-first=%d projector=%d, want 2 each", len(relayFirst), len(sink.events))
	}
	assertSameRecord(t, relayFirst[0], sink.events[0])
	assertSameRecord(t, relayFirst[1], sink.events[1])
	assertCPStateEnvelope(t, relayFirst[0], KindServiceRegistry, serviceID.String(), true, kinds.CPStateTopicServiceRegistry)
	assertCPStateEnvelope(t, relayFirst[1], KindEnvironmentRegistry, environmentID.String(), true, kinds.CPStateTopicEnvironmentRegistry)
	assertWebReadModelFilterMatches(t, relayFirst[0])
	assertWebReadModelFilterMatches(t, relayFirst[1])
}

// Both writers stamp created_at from one per-coordinate floor, so whichever
// signs next on a coordinate is strictly newer, even within one second: a
// tombstone never loses a created_at tie to the live record it replaces.
func TestRelayFirstAndProjectorShareTheCoordinateCreatedAtFloor(t *testing.T) {
	// The projector does not publish service registry records.
	// Verify that successive relay-first writes on the same coordinate
	// produce strictly increasing created_at values.
	ctx := context.Background()
	h := newRelayFirstHarness(t, false)
	created, updated := relayFirstTestTimes()
	stored := domain.Service{ID: domain.NewEntityID(), Name: "api", RuntimeType: domain.RuntimeTypeDocker, DefaultBranch: "main", CreatedAt: created, UpdatedAt: updated}
	h.services.rows[stored.ID] = stored

	writer := NewRelayFirstStatePublisher(h.projector, newRelayFirstTestPublisher(h.relayFirst))

	// Live record via relay-first.
	if err := writer.PublishServiceRegistry(ctx, &stored, false); err != nil {
		t.Fatalf("relay-first live record: %v", err)
	}
	// Tombstone on the same coordinate.
	if err := writer.PublishServiceRegistry(ctx, &domain.Service{ID: stored.ID, UpdatedAt: time.Now().UTC()}, true); err != nil {
		t.Fatalf("relay-first tombstone: %v", err)
	}

	if len(h.relayFirst.events) != 2 {
		t.Fatalf("relay-first events = %d, want 2", len(h.relayFirst.events))
	}
	live, tombstone := h.relayFirst.events[0], h.relayFirst.events[1]
	if !(live.CreatedAt < tombstone.CreatedAt) {
		t.Fatalf("created_at not strictly increasing: live=%d tombstone=%d", live.CreatedAt, tombstone.CreatedAt)
	}
	if eventDTag(live) != eventDTag(tombstone) {
		t.Fatalf("tombstone left the live coordinate: %q vs %q", eventDTag(live), eventDTag(tombstone))
	}
}

// The relay-first service update publishes before the cache stamps the new
// revision. updated_at is the revision clients send back, so the projection
// of the stamped row must replace the relay-first record rather than be
// deduplicated against it.
func TestRelayFirstRecordIsReplacedWhenTheCacheStampsANewRevision(t *testing.T) {
	// The projector does not publish service registry records.
	// Verify the relay-first update publishes exactly one record with the
	// correct cached revision.
	ctx := context.Background()
	h := newRelayFirstHarness(t, true)
	created, updated := relayFirstTestTimes()
	stored := domain.Service{ID: domain.NewEntityID(), Name: "api", RuntimeType: domain.RuntimeTypeDocker, DefaultBranch: "main", CreatedAt: created, UpdatedAt: updated}
	h.services.rows[stored.ID] = stored

	edit := stored
	edit.Name = "api-v2"
	if err := h.registry.UpdateService(ctx, &edit); err != nil {
		t.Fatalf("relay-first UpdateService: %v", err)
	}
	// The projector does not handle service events.
	h.projector.handleEvent(ctx, events.Event{Type: events.EventServiceUpdated, EntityID: stored.ID.String()})
	_ = ctx // keep linter happy

	if len(h.relayFirst.events) != 1 {
		t.Fatalf("relay-first published %d events, want 1", len(h.relayFirst.events))
	}
	projected := h.projectorRelay.byKind(KindServiceRegistry)
	if len(projected) != 0 {
		t.Fatalf("projector published %d service records after F2 leg deletion, want 0", len(projected))
	}

	var content struct {
		UpdatedAt string `json:"updated_at"`
	}
	if err := json.Unmarshal([]byte(h.relayFirst.events[0].Content), &content); err != nil {
		t.Fatal(err)
	}
	// The relay-first record carries the revision minted by prepareServiceUpdate,
	// which is newer than the original. The projector does not re-project.
	parsed, err := time.Parse(time.RFC3339Nano, content.UpdatedAt)
	if err != nil {
		t.Fatalf("parse relay-first updated_at %q: %v", content.UpdatedAt, err)
	}
	if !parsed.After(updated) {
		t.Fatalf("relay-first revision %s did not advance past original %s", parsed, updated)
	}
}

// A create publishes before the cache mints the timestamps. The record omits
// them instead of carrying "", which the daemon catalog cannot decode.
func TestRelayFirstCreateRecordWithoutTimestampsDecodes(t *testing.T) {
	ctx := context.Background()
	h := newRelayFirstHarness(t, false)
	if err := h.registry.CreateService(ctx, &domain.Service{ID: domain.NewEntityID(), Name: "api"}); err != nil {
		t.Fatalf("relay-first CreateService: %v", err)
	}
	ev := h.relayFirst.events[0]
	decoded, err := decodeServiceProjection(&ev)
	if err != nil {
		t.Fatalf("catalog decode of the relay-first create record: %v (content %s)", err, ev.Content)
	}
	if decoded.Service == nil || decoded.Service.Name != "api" {
		t.Fatalf("decoded service = %+v", decoded.Service)
	}
}

func TestRelayFirstWriteFailsWithoutRelayAcceptanceAndIsNotRemembered(t *testing.T) {
	ctx := context.Background()
	h := newRelayFirstHarness(t, false)
	created, updated := relayFirstTestTimes()
	stored := domain.Service{ID: domain.NewEntityID(), Name: "api", RuntimeType: domain.RuntimeTypeDocker, DefaultBranch: "main", CreatedAt: created, UpdatedAt: updated}
	h.services.rows[stored.ID] = stored
	h.relayFirst.zeroAcceptedKind = map[int]bool{KindCASControlState: true}

	edit := stored
	edit.Name = "api-v2"
	if err := h.registry.UpdateService(ctx, &edit); err == nil {
		t.Fatal("relay-first UpdateService succeeded without a relay accepting the record")
	}
	if h.services.rows[stored.ID].Name != "api" {
		t.Fatal("cache written after the relay rejected the record")
	}
	// The projector does not publish service records. After a rejected
	// relay-first write, there is no fallback projector publish.
	h.projector.handleEvent(ctx, events.Event{Type: events.EventServiceUpdated, EntityID: stored.ID.String()})
	if got := h.projectorRelay.byKind(KindServiceRegistry); len(got) != 0 {
		t.Fatalf("projector published %d service records on service event, want 0", len(got))
	}
	_ = ctx
}

func TestProjectionFingerprintKeepsRegistryRevision(t *testing.T) {
	registry := gonostr.Tags{{"d", "x"}, {"legacy_kind", strconv.Itoa(KindServiceRegistry)}}
	if projectionFingerprint(KindCASControlState, registry, `{"id":"x","updated_at":"t1"}`) ==
		projectionFingerprint(KindCASControlState, registry, `{"id":"x","updated_at":"t2"}`) {
		t.Fatal("service-registry fingerprint ignores updated_at, the client revision token")
	}
	state := gonostr.Tags{{"d", "x"}, {"legacy_kind", strconv.Itoa(KindServiceState)}}
	if projectionFingerprint(KindCASControlState, state, `{"id":"x","updated_at":"t1"}`) !=
		projectionFingerprint(KindCASControlState, state, `{"id":"x","updated_at":"t2"}`) {
		t.Fatal("service-state fingerprint must not treat updated_at as volatile")
	}
}

func assertSameRecord(t *testing.T, got, want gonostr.Event) {
	t.Helper()
	if got.Kind != want.Kind || got.PubKey != want.PubKey {
		t.Fatalf("kind/author differ: relay-first %d/%s, projector %d/%s", got.Kind, got.PubKey.Hex(), want.Kind, want.PubKey.Hex())
	}
	if !reflect.DeepEqual(got.Tags, want.Tags) {
		t.Fatalf("tags differ:\nrelay-first %v\nprojector   %v", got.Tags, want.Tags)
	}
	if got.Content != want.Content {
		t.Fatalf("content differs:\nrelay-first %s\nprojector   %s", got.Content, want.Content)
	}
}

func assertCPStateEnvelope(t *testing.T, ev gonostr.Event, legacyKind int, d string, deleted bool, topic string) {
	t.Helper()
	if int(ev.Kind) != KindCASControlState {
		t.Fatalf("kind = %d, want %d", ev.Kind, KindCASControlState)
	}
	family := cpStateFamilies[legacyKind]
	for name, value := range map[string]string{
		"d": d, "domain": family.domain, "schema": controlStateSchema,
		"legacy_kind": strconv.Itoa(legacyKind), "deleted": strconv.FormatBool(deleted), "t": topic,
	} {
		if !hasTag(ev.Tags, name, value) {
			t.Fatalf("missing envelope tag %s=%s in %v", name, value, ev.Tags)
		}
	}
	if hasTag(ev.Tags, "entity", "registry") {
		t.Fatalf("relay-first-only entity tag is back: %v", ev.Tags)
	}
}

// assertWebReadModelFilterMatches applies the web control-plane read model's
// cp-state REQ shape ({kinds:[30900], authors:[service], "#t":[...]}).
func assertWebReadModelFilterMatches(t *testing.T, ev gonostr.Event) {
	t.Helper()
	filter := gonostr.Filter{
		Kinds:   []gonostr.Kind{gonostr.Kind(KindCASControlState)},
		Authors: []gonostr.PubKey{ev.PubKey},
		Tags:    gonostr.TagMap{"t": {kinds.CPStateTopicServiceRegistry, kinds.CPStateTopicEnvironmentRegistry}},
	}
	if !filter.Matches(ev) {
		t.Fatalf("web #t read-model filter does not match the record: %v", ev.Tags)
	}
}

// relayFirstTestServiceRepo is the registry's service cache. With
// stampRevision it stamps updated_at on update, as PgServiceRepository does.
type relayFirstTestServiceRepo struct {
	mu            sync.Mutex
	rows          map[uuid.UUID]domain.Service
	stampRevision bool
	// pgRevisions applies PgServiceRepository.Update's revision rule.
	pgRevisions bool
}

func (r *relayFirstTestServiceRepo) Create(_ context.Context, svc *domain.Service) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows[svc.ID] = *svc
	return nil
}

func (r *relayFirstTestServiceRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Service, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	svc, ok := r.rows[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return &svc, nil
}

func (r *relayFirstTestServiceRepo) GetByName(context.Context, string) (*domain.Service, error) {
	return nil, repository.ErrNotFound
}

func (r *relayFirstTestServiceRepo) List(context.Context) ([]domain.Service, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.Service, 0, len(r.rows))
	for _, svc := range r.rows {
		out = append(out, svc)
	}
	return out, nil
}

func (r *relayFirstTestServiceRepo) ListByOrg(context.Context, uuid.UUID) ([]domain.Service, error) {
	return nil, nil
}

func (r *relayFirstTestServiceRepo) Update(_ context.Context, svc *domain.Service) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stampRevision {
		svc.UpdatedAt = r.rows[svc.ID].UpdatedAt.Add(time.Second + 7*time.Microsecond)
	}
	if r.pgRevisions {
		svc.UpdatedAt = pgStoredRevision(r.rows[svc.ID].UpdatedAt, svc.UpdatedAt)
	}
	r.rows[svc.ID] = *svc
	return nil
}

// pgStoredRevision is the revision repository.revisionAssignment stores: a
// newer requested revision verbatim, otherwise one moved past the stored one.
func pgStoredRevision(stored, requested time.Time) time.Time {
	requested = domain.NormalizeRevisionTime(requested)
	if requested.After(stored) {
		return requested
	}
	return domain.NextRevisionTime(stored)
}

func (r *relayFirstTestServiceRepo) Delete(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.rows, id)
	return nil
}

type relayFirstTestEnvironmentRepo struct {
	mu   sync.Mutex
	rows map[uuid.UUID]domain.Environment
	// pgRevisions applies PgEnvironmentRepository.Update's revision rule.
	pgRevisions bool
}

func (r *relayFirstTestEnvironmentRepo) Create(_ context.Context, env *domain.Environment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows[env.ID] = *env
	return nil
}

func (r *relayFirstTestEnvironmentRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Environment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	env, ok := r.rows[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return &env, nil
}

func (r *relayFirstTestEnvironmentRepo) GetByName(context.Context, string) (*domain.Environment, error) {
	return nil, repository.ErrNotFound
}

func (r *relayFirstTestEnvironmentRepo) List(context.Context) ([]domain.Environment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.Environment, 0, len(r.rows))
	for _, env := range r.rows {
		out = append(out, env)
	}
	return out, nil
}

func (r *relayFirstTestEnvironmentRepo) ListByOrg(context.Context, uuid.UUID) ([]domain.Environment, error) {
	return nil, nil
}

func (r *relayFirstTestEnvironmentRepo) Update(_ context.Context, env *domain.Environment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pgRevisions {
		env.UpdatedAt = pgStoredRevision(r.rows[env.ID].UpdatedAt, env.UpdatedAt)
	}
	r.rows[env.ID] = *env
	return nil
}

func (r *relayFirstTestEnvironmentRepo) Delete(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.rows, id)
	return nil
}

// relayFirstOutboxRegistry is the relay-first registry over a control-plane
// publisher with a local outbox (the production wiring), with one cached
// service it can update.
func relayFirstOutboxRegistry(t *testing.T, publisher *Publisher) (*service.RelayFirstRegistry, *relayFirstTestServiceRepo, domain.Service) {
	t.Helper()
	services := &relayFirstTestServiceRepo{rows: map[uuid.UUID]domain.Service{}}
	created, updated := relayFirstTestTimes()
	stored := domain.Service{ID: domain.NewEntityID(), Name: "api", RuntimeType: domain.RuntimeTypeDocker, DefaultBranch: "main", CreatedAt: created, UpdatedAt: updated}
	services.rows[stored.ID] = stored
	source := service.NewRegistryService(services, &relayFirstTestEnvironmentRepo{rows: map[uuid.UUID]domain.Environment{}}, nil, nil, nil, nil, nil, nil, nil, &events.NoopPublisher{}, zap.NewNop())
	projector := newRelayFirstTestProjector(source, nil)
	return service.NewRelayFirstRegistry(source, NewRelayFirstStatePublisher(projector, publisher), zap.NewNop()), services, stored
}

// Quorum 1 with one control-plane relay down: the write commits on the up
// relay's OK, and the outbox then retries only the down relay until it
// accepts. The relay that accepted in the pre-commit round is never contacted
// again.
func TestRelayFirstQuorumMetRetriesTheDownRelayThroughTheOutbox(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), syncTestTimeout)
	defer cancel()
	up := startSyncTestRelay(t, syncTestRelayOptions{})
	down := startSyncTestRelay(t, syncTestRelayOptions{})
	down.down.Store(true)
	pool := newSyncTestPool(up, down)
	defer pool.Close()
	h := newLocalOutboxHarness(t, t.TempDir(), pool, projectorTestPrivateKey, 0)
	h.startRunner()
	registry, services, stored := relayFirstOutboxRegistry(t, h.pub)

	edit := stored
	edit.Name = "api-v2"
	require.NoError(t, registry.UpdateService(ctx, &edit), "the default quorum of one relay accepted")
	require.Equal(t, "api-v2", services.rows[stored.ID].Name, "the cache is written once the quorum accepted")

	first := receive(t, h.rounds, "the pre-commit round")
	require.ElementsMatch(t, []string{up.url, down.url}, first.targets, "the pre-commit round goes to every control-plane relay")
	ev := receive(t, h.delivered, "the delivered hook")
	require.Equal(t, KindCASControlState, int(ev.Kind))
	entry := h.entry(t, ev.ID)
	require.Equal(t, localstore.OutboxPending, entry.State, "pending until the down relay accepts")
	require.True(t, entry.Delivered)
	require.Equal(t, 1, entry.Rounds, "the pre-commit round counts against the attempt budget")
	require.True(t, entry.Relays[up.url].Accepted, "the up relay's OK is recorded")
	require.False(t, entry.Relays[down.url].Accepted)
	counts, err := h.outbox.Counts()
	require.NoError(t, err)
	require.Equal(t, int64(1), counts.Pending, "outbox depth sees the relay-first record")
	require.True(t, h.storeHolds(ev.ID), "the record is kept as the daemon's own output")

	down.down.Store(false)
	for accepted := false; !accepted; {
		round := receive(t, h.rounds, "a retry round")
		require.Equal(t, []string{down.url}, round.targets, "a retry contacted a relay that already accepted")
		for _, result := range round.results {
			accepted = accepted || (result.RelayURL == down.url && (result.Accepted || result.IsDuplicate()))
		}
	}
	h.stopRun()
	h.stopRun = nil
	entry = h.entry(t, ev.ID)
	require.Equal(t, localstore.OutboxPublished, entry.State)
	require.True(t, entry.Relays[up.url].Accepted)
	require.True(t, entry.Relays[down.url].Accepted)
}

// Below the quorum the write is abandoned: the cache is not written and the
// record is not queued, so no relay is sent it later for a state that was
// never committed.
func TestRelayFirstQuorumNotMetQueuesNothingAndSkipsTheCacheWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), syncTestTimeout)
	defer cancel()
	up := startSyncTestRelay(t, syncTestRelayOptions{})
	down := startSyncTestRelay(t, syncTestRelayOptions{})
	down.down.Store(true)
	pool := newSyncTestPool(up, down)
	defer pool.Close()
	h := newLocalOutboxHarness(t, t.TempDir(), pool, projectorTestPrivateKey, config.PublishQuorumAllRelays)
	registry, services, stored := relayFirstOutboxRegistry(t, h.pub)

	edit := stored
	edit.Name = "api-v2"
	err := registry.UpdateService(ctx, &edit)
	require.Error(t, err, "one of two required relays accepted")
	require.Contains(t, err.Error(), "not queued")
	require.Equal(t, "api", services.rows[stored.ID].Name, "the cache was written below the quorum")

	round := receive(t, h.rounds, "the pre-commit round")
	require.ElementsMatch(t, []string{up.url, down.url}, round.targets)
	require.Empty(t, h.rounds, "a relay was contacted again")
	counts, err := h.outbox.Counts()
	require.NoError(t, err)
	require.Equal(t, localstore.OutboxCounts{}, counts, "the rejected record was queued")
	require.Empty(t, h.delivered)
}

// build, artifact, deployment intent and
// deployment run families publish canonical cp-state through the authoritative
// projection path, not through the projector's event handler.

func TestAuthoritativeBuildRegistryProducesOneRecord(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	writer := NewRelayFirstStatePublisher(newRelayFirstTestProjector(nil, sink), newRelayFirstTestPublisher(sink))

	build := &domain.Build{
		ID:        domain.NewEntityID(),
		ServiceID: uuid.New(),
		GitSHA:    "abc123",
		GitRef:    "refs/heads/main",
		Status:    domain.BuildStatusSucceeded,
		CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, writer.PublishBuildRegistry(ctx, build, false))
	records := sink.byKind(KindCASControlState)
	require.Len(t, records, 1, "exactly one canonical 30900 per build mutation")
	assertCPStateEnvelope(t, records[0], KindBuildRegistry, build.ID.String(), false, kinds.CPStateTopicBuildRegistry)

	var content map[string]any
	require.NoError(t, json.Unmarshal([]byte(records[0].Content), &content))
	require.Equal(t, build.ID.String(), content["id"])
	require.Equal(t, build.ServiceID.String(), content["service_id"])
	require.Equal(t, "abc123", content["git_sha"])
	require.Equal(t, string(domain.BuildStatusSucceeded), content["status"])
}

func TestAuthoritativeArtifactRegistryProducesOneRecord(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	writer := NewRelayFirstStatePublisher(newRelayFirstTestProjector(nil, sink), newRelayFirstTestPublisher(sink))

	artifact := &domain.Artifact{
		ID:          domain.NewEntityID(),
		BuildID:     uuid.New(),
		ServiceID:   uuid.New(),
		ImageRepo:   "ghcr.io/openagents/worker",
		ImageTag:    "v1.2.3",
		ImageDigest: "sha256:deadbeef",
		CreatedAt:   time.Now().UTC(),
	}
	require.NoError(t, writer.PublishArtifactRegistry(ctx, artifact, false))
	records := sink.byKind(KindCASControlState)
	require.Len(t, records, 1, "exactly one canonical 30900 per artifact mutation")
	assertCPStateEnvelope(t, records[0], KindArtifactRegistry, artifact.ID.String(), false, kinds.CPStateTopicArtifactRegistry)

	var content map[string]any
	require.NoError(t, json.Unmarshal([]byte(records[0].Content), &content))
	require.Equal(t, artifact.ID.String(), content["id"])
	require.Equal(t, "ghcr.io/openagents/worker", content["image_repo"])
	require.Equal(t, "sha256:deadbeef", content["image_digest"])
}

func TestAuthoritativeDeploymentIntentRegistryCarriesDesiredHash(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	writer := NewRelayFirstStatePublisher(newRelayFirstTestProjector(nil, sink), newRelayFirstTestPublisher(sink))

	intent := &domain.DeploymentIntent{
		ID:            domain.NewEntityID(),
		ServiceID:     uuid.New(),
		EnvironmentID: uuid.New(),
		ArtifactID:    uuid.New(),
		Status:        domain.IntentStatusDeploying,
		DesiredHash:   "sha256:intent-hash",
		DesiredState: &domain.DesiredServiceSpec{
			StableServiceKey: "api-prod",
			DockerExtension:  &domain.DockerExtension{},
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, writer.PublishDeploymentIntentRegistry(ctx, intent, false))
	records := sink.byKind(KindCASControlState)
	require.Len(t, records, 1, "exactly one canonical 30900 per intent mutation")
	assertCPStateEnvelope(t, records[0], KindDeploymentIntentRegistry, intent.ID.String(), false, kinds.CPStateTopicDeploymentIntent)

	ev := records[0]
	require.True(t, hasTag(ev.Tags, "desired_hash", "sha256:intent-hash"), "desired_hash tag present")
	var content map[string]any
	require.NoError(t, json.Unmarshal([]byte(ev.Content), &content))
	require.Equal(t, "sha256:intent-hash", content["desired_hash"])
	require.Equal(t, "docker", content["renderer"])
	require.Equal(t, "api-prod", content["target"])
}

func TestAuthoritativeDeploymentRunRegistryCarriesApplyMetadata(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	writer := NewRelayFirstStatePublisher(newRelayFirstTestProjector(nil, sink), newRelayFirstTestPublisher(sink))

	obsID := uuid.New()
	run := &domain.DeploymentRun{
		ID:                 domain.NewEntityID(),
		DeploymentIntentID: uuid.New(),
		Status:             domain.RunStatusSucceeded,
		ApplyMetadata: map[string]any{
			"renderer":       "compose",
			"desired_hash":   "sha256:run-hash",
			"revision_hash":  "sha256:rev-hash",
			"target":         "api-prod",
			"apply_summary":  "recreated 1 service",
			"observation_id": obsID.String(),
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, writer.PublishDeploymentRunRegistry(ctx, run, false))
	records := sink.byKind(KindCASControlState)
	require.Len(t, records, 1, "exactly one canonical 30900 per run mutation")
	assertCPStateEnvelope(t, records[0], KindDeploymentRunRegistry, run.ID.String(), false, kinds.CPStateTopicDeploymentRun)

	ev := records[0]
	require.True(t, hasTag(ev.Tags, "renderer", "compose"), "renderer tag from apply_metadata")
	var content map[string]any
	require.NoError(t, json.Unmarshal([]byte(ev.Content), &content))
	require.Equal(t, "compose", content["renderer"])
	require.Equal(t, "sha256:run-hash", content["desired_hash"])
	require.Equal(t, "sha256:rev-hash", content["revision_hash"])
	require.Equal(t, "api-prod", content["target"])
	require.Equal(t, "recreated 1 service", content["apply_summary"])
	require.Equal(t, obsID.String(), content["observation_id"])
}

func TestAuthoritativeRunRegistryOmitsApplyMetadataWhenNil(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	writer := NewRelayFirstStatePublisher(newRelayFirstTestProjector(nil, sink), newRelayFirstTestPublisher(sink))

	run := &domain.DeploymentRun{
		ID:                 domain.NewEntityID(),
		DeploymentIntentID: uuid.New(),
		Status:             domain.RunStatusSucceeded,
		CreatedAt:          time.Now().UTC(),
		UpdatedAt:          time.Now().UTC(),
	}
	require.NoError(t, writer.PublishDeploymentRunRegistry(ctx, run, false))
	records := sink.byKind(KindCASControlState)
	require.Len(t, records, 1)
	ev := records[0]
	require.False(t, hasTag(ev.Tags, "renderer", ""), "no renderer tag when apply_metadata is nil")
	var content map[string]any
	require.NoError(t, json.Unmarshal([]byte(ev.Content), &content))
	_, hasRenderer := content["renderer"]
	require.False(t, hasRenderer, "content should not have renderer when apply_metadata is nil")
}

func TestAuthoritativeFingerprintDedupSuppressesUnchangedRepeats(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	writer := NewRelayFirstStatePublisher(newRelayFirstTestProjector(nil, sink), newRelayFirstTestPublisher(sink))

	build := &domain.Build{
		ID:        domain.NewEntityID(),
		ServiceID: uuid.New(),
		GitSHA:    "abc123",
		Status:    domain.BuildStatusSucceeded,
		CreatedAt: time.Now().UTC(),
	}
	// First publish: 1 record.
	require.NoError(t, writer.PublishBuildRegistry(ctx, build, false))
	require.Len(t, sink.byKind(KindCASControlState), 1)

	// Same entity, same state: fingerprint dedup suppresses.
	require.NoError(t, writer.PublishBuildRegistry(ctx, build, false))
	require.Len(t, sink.byKind(KindCASControlState), 1, "unchanged repeat should be suppressed by fingerprint dedup")

	// Mutate status: new record.
	build.Status = domain.BuildStatusFailed
	require.NoError(t, writer.PublishBuildRegistry(ctx, build, false))
	require.Len(t, sink.byKind(KindCASControlState), 2, "changed state should produce a new record")
}

func TestAuthoritativeTombstonesProduceDeletedRecord(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	writer := NewRelayFirstStatePublisher(newRelayFirstTestProjector(nil, sink), newRelayFirstTestPublisher(sink))

	build := &domain.Build{ID: domain.NewEntityID(), ServiceID: uuid.New(), CreatedAt: time.Now().UTC()}
	require.NoError(t, writer.PublishBuildRegistry(ctx, build, true))
	records := sink.byKind(KindCASControlState)
	require.Len(t, records, 1)
	assertCPStateEnvelope(t, records[0], KindBuildRegistry, build.ID.String(), true, kinds.CPStateTopicBuildRegistry)

	var content map[string]any
	require.NoError(t, json.Unmarshal([]byte(records[0].Content), &content))
	require.Equal(t, true, content["deleted"])
}
