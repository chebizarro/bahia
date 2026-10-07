package nostr

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// bahia-irsry.53: the relay-first registry mints created_at/updated_at (and
// explicit deployment-unit ids) before it publishes, and the cache keeps
// them, so every create and update is signed exactly once: the projection
// of the stored row is the relay-first record. Environment records carry the
// explicit unit set from both writers.

// newRelayFirstUnitHarness is newRelayFirstHarness with transactional
// repositories, which the revision-checked updates and explicit deployment
// units need.
func newRelayFirstUnitHarness(t *testing.T) (*relayFirstHarness, *relayFirstTestUnitRepo) {
	t.Helper()
	h := &relayFirstHarness{
		services:       &relayFirstTestServiceRepo{rows: map[uuid.UUID]domain.Service{}, pgRevisions: true},
		environments:   &relayFirstTestEnvironmentRepo{rows: map[uuid.UUID]domain.Environment{}, pgRevisions: true},
		projectorRelay: &captureProjectionPublisher{},
		relayFirst:     &captureProjectionPublisher{},
	}
	units := &relayFirstTestUnitRepo{rows: map[uuid.UUID]domain.DeploymentUnit{}}
	tx := relayFirstTestTx{services: h.services, environments: h.environments, units: units}
	h.source = service.NewRegistryService(h.services, h.environments, nil, nil, nil, nil, nil, nil, nil, &events.NoopPublisher{}, zap.NewNop(), service.WithRegistryTxExecutor(tx))
	h.projector = newRelayFirstTestProjector(h.source, h.projectorRelay)
	h.registry = service.NewRelayFirstRegistry(h.source, NewRelayFirstStatePublisher(h.projector, newRelayFirstTestPublisher(h.relayFirst)), zap.NewNop())
	return h, units
}

// signaturesOn counts the events signed on one registry coordinate.
func signaturesOn(sink *captureProjectionPublisher, legacyKind int, id uuid.UUID) int {
	n := 0
	for _, ev := range sink.byKind(legacyKind) {
		if eventDTag(ev) == id.String() {
			n++
		}
	}
	return n
}

// latestOn returns the newest event signed on a registry coordinate.
func latestOn(t *testing.T, sink *captureProjectionPublisher, legacyKind int, id uuid.UUID) gonostr.Event {
	t.Helper()
	var latest *gonostr.Event
	for _, ev := range sink.byKind(legacyKind) {
		if eventDTag(ev) == id.String() {
			ev := ev
			latest = &ev
		}
	}
	if latest == nil {
		t.Fatalf("no event signed on %s", id)
	}
	return *latest
}

type registryRecordContent struct {
	UpdatedAt       time.Time               `json:"updated_at"`
	CreatedAt       time.Time               `json:"created_at"`
	DeploymentUnits []domain.DeploymentUnit `json:"deployment_units"`
}

func decodeRegistryRecord(t *testing.T, ev gonostr.Event) registryRecordContent {
	t.Helper()
	var content registryRecordContent
	if err := json.Unmarshal([]byte(ev.Content), &content); err != nil {
		t.Fatalf("decode registry record: %v (%s)", err, ev.Content)
	}
	return content
}

// assertSignedOnce checks that the projector, reacting to the cache write's
// bus event and to a full snapshot, signs nothing on top of the relay-first
// writes: every write so far is exactly one signature.
func assertSignedOnce(t *testing.T, h *relayFirstHarness, eventType events.EventType, legacyKind int, id uuid.UUID, writes int) {
	t.Helper()
	h.projector.handleEvent(context.Background(), events.Event{Type: eventType, EntityID: id.String()})
	h.republishRegistrySnapshot(t)
	if got := signaturesOn(h.relayFirst, legacyKind, id); got != writes {
		t.Fatalf("relay-first signed %d events on %s, want %d (one per write)", got, id, writes)
	}
	if got := signaturesOn(h.projectorRelay, legacyKind, id); got != 0 {
		t.Fatalf("projector re-signed %d events on %s after %s; want 0 (records %s)", got, id, eventType, latestOn(t, h.projectorRelay, legacyKind, id).Content)
	}
}

func TestRelayFirstServiceCreateAndUpdatesAreSignedOnce(t *testing.T) {
	ctx := context.Background()
	h, _ := newRelayFirstUnitHarness(t)

	svc := &domain.Service{ID: domain.NewEntityID(), Name: "payments-api", ArtifactRepo: "registry.example/payments"}
	if err := h.registry.CreateService(ctx, svc); err != nil {
		t.Fatalf("relay-first CreateService: %v", err)
	}
	assertSignedOnce(t, h, events.EventServiceCreated, KindServiceRegistry, svc.ID, 1)
	created := decodeRegistryRecord(t, latestOn(t, h.relayFirst, KindServiceRegistry, svc.ID))
	stored := h.services.rows[svc.ID]
	if created.CreatedAt.IsZero() || !created.CreatedAt.Equal(stored.CreatedAt) || !created.UpdatedAt.Equal(stored.UpdatedAt) {
		t.Fatalf("create record timestamps created_at=%s updated_at=%s, want the stored %s/%s", created.CreatedAt, created.UpdatedAt, stored.CreatedAt, stored.UpdatedAt)
	}

	// A plain update, then a revision-checked one whose token is read back
	// from the relay record (as a client does).
	edit := stored
	edit.DefaultBranch = "release"
	if err := h.registry.UpdateService(ctx, &edit); err != nil {
		t.Fatalf("relay-first UpdateService: %v", err)
	}
	assertSignedOnce(t, h, events.EventServiceUpdated, KindServiceRegistry, svc.ID, 2)

	token := decodeRegistryRecord(t, latestOn(t, h.relayFirst, KindServiceRegistry, svc.ID)).UpdatedAt
	edit = h.services.rows[svc.ID]
	edit.ArtifactRepo = "registry.example/payments-v2"
	if err := h.registry.UpdateServiceWithExpectedRevision(ctx, &edit, token); err != nil {
		t.Fatalf("revision-checked update with the relay record's token: %v", err)
	}
	assertSignedOnce(t, h, events.EventServiceUpdated, KindServiceRegistry, svc.ID, 3)
	if got := decodeRegistryRecord(t, latestOn(t, h.relayFirst, KindServiceRegistry, svc.ID)).UpdatedAt; !got.Equal(h.services.rows[svc.ID].UpdatedAt) || !got.After(token) {
		t.Fatalf("record revision %s, want the stored revision %s, after %s", got, h.services.rows[svc.ID].UpdatedAt, token)
	}
}

func TestRelayFirstEnvironmentCreateAndUpdatesAreSignedOnceWithExplicitUnits(t *testing.T) {
	ctx := context.Background()
	h, units := newRelayFirstUnitHarness(t)

	env := &domain.Environment{
		ID: domain.NewEntityID(), Name: "prod", OrgID: uuid.New(),
		Targeting: domain.EnvironmentTargeting{DefaultUnitKey: "web"},
	}
	requested := []*domain.DeploymentUnit{
		{Key: "worker", DisplayName: "Workers", RuntimeType: domain.RuntimeTypeCompose, ComposeDir: "/srv/worker", ReconcileMode: domain.ReconcileModeApprovalRequired, OwnershipMode: domain.OwnershipModeBahiaManaged, RuntimeConfig: map[string]any{"execution_mode": "cli"}},
		{Key: "web", RuntimeType: domain.RuntimeTypeCompose, ComposeDir: "/srv/web", ReconcileMode: domain.ReconcileModeObserveOnly, OwnershipMode: domain.OwnershipModeBahiaManaged, NetworkProfile: map[string]string{"network": "edge"}},
	}
	if err := h.registry.CreateEnvironmentWithDeploymentUnits(ctx, env, requested); err != nil {
		t.Fatalf("relay-first CreateEnvironmentWithDeploymentUnits: %v", err)
	}
	assertSignedOnce(t, h, events.EventEnvironmentCreated, KindEnvironmentRegistry, env.ID, 1)

	// The record's explicit units round-trip: the stored units' ids and
	// declared fields and timestamps, sorted by key.
	record := decodeRegistryRecord(t, latestOn(t, h.relayFirst, KindEnvironmentRegistry, env.ID))
	storedUnits, err := h.source.ListEnvironmentDeploymentUnits(ctx, env.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertRecordUnits(t, record.DeploymentUnits, storedUnits, "web", "worker")
	for _, unit := range storedUnits {
		if unit.ID.Version() != 7 {
			t.Fatalf("unit %q id %s is not a daemon-minted UUIDv7", unit.Key, unit.ID)
		}
	}
	ev := latestOn(t, h.relayFirst, KindEnvironmentRegistry, env.ID)
	if tagValue(ev.Tags, "unit") != "web" {
		t.Fatalf("unit tag = %q, want the default unit key", tagValue(ev.Tags, "unit"))
	}

	// A complete-set update with the relay record's revision token: one unit
	// kept (same id), one added, one removed.
	token := record.UpdatedAt
	edit := h.environments.rows[env.ID]
	edit.Protected = true
	next := []*domain.DeploymentUnit{
		{Key: "web", RuntimeType: domain.RuntimeTypeCompose, ComposeDir: "/srv/web-v2", ReconcileMode: domain.ReconcileModeObserveOnly, OwnershipMode: domain.OwnershipModeBahiaManaged},
		{Key: "batch", RuntimeType: domain.RuntimeTypeCompose, ComposeDir: "/srv/batch", ReconcileMode: domain.ReconcileModeObserveOnly, OwnershipMode: domain.OwnershipModeBahiaManaged},
	}
	if err := h.registry.UpdateEnvironmentWithDeploymentUnits(ctx, &edit, next, token); err != nil {
		t.Fatalf("revision-checked unit update with the relay record's token: %v", err)
	}
	assertSignedOnce(t, h, events.EventEnvironmentUpdated, KindEnvironmentRegistry, env.ID, 2)
	updated := decodeRegistryRecord(t, latestOn(t, h.relayFirst, KindEnvironmentRegistry, env.ID))
	storedUnits, _ = h.source.ListEnvironmentDeploymentUnits(ctx, env.ID)
	assertRecordUnits(t, updated.DeploymentUnits, storedUnits, "batch", "web")
	if web := unitByKey(storedUnits, "web"); web.ID != unitByKey(record.DeploymentUnits, "web").ID {
		t.Fatalf("kept unit web changed id %s -> %s", unitByKey(record.DeploymentUnits, "web").ID, web.ID)
	}

	// A plain update keeps the stored units, and the record still carries
	// them, so the projection of the row is again not re-signed.
	edit = h.environments.rows[env.ID]
	edit.DeployStrategy = domain.DeployStrategyBlueGreen
	if err := h.registry.UpdateEnvironment(ctx, &edit); err != nil {
		t.Fatalf("relay-first UpdateEnvironment: %v", err)
	}
	assertSignedOnce(t, h, events.EventEnvironmentUpdated, KindEnvironmentRegistry, env.ID, 3)
	assertRecordUnits(t, decodeRegistryRecord(t, latestOn(t, h.relayFirst, KindEnvironmentRegistry, env.ID)).DeploymentUnits, storedUnits, "batch", "web")
	if len(units.rows) != 2 {
		t.Fatalf("stored units = %d, want 2", len(units.rows))
	}
}

func TestRelayFirstEnvironmentWithoutExplicitUnitsCarriesTheImplicitDefault(t *testing.T) {
	ctx := context.Background()
	h, _ := newRelayFirstUnitHarness(t)
	env := &domain.Environment{ID: domain.NewEntityID(), Name: "staging"}
	if err := h.registry.CreateEnvironment(ctx, env); err != nil {
		t.Fatalf("relay-first CreateEnvironment: %v", err)
	}
	assertSignedOnce(t, h, events.EventEnvironmentCreated, KindEnvironmentRegistry, env.ID, 1)
	record := decodeRegistryRecord(t, latestOn(t, h.relayFirst, KindEnvironmentRegistry, env.ID))
	if len(record.DeploymentUnits) != 1 || !record.DeploymentUnits[0].Implicit || record.DeploymentUnits[0].Key != domain.DefaultDeploymentUnitKey || record.DeploymentUnits[0].ID != uuid.Nil {
		t.Fatalf("deployment_units = %+v, want only the implicit default unit", record.DeploymentUnits)
	}
}

// The projector refuses to publish an environment record whose units it
// cannot read, rather than replacing the real units with the default.
func TestProjectorEnvironmentRecordFailsWhenUnitsCannotBeRead(t *testing.T) {
	sink := &captureProjectionPublisher{}
	projector := newRelayFirstTestProjector(failingUnitSource{}, sink)
	err := projector.publishEnvironmentRegistry(context.Background(), &domain.Environment{ID: domain.NewEntityID(), Name: "prod"}, false)
	if err == nil || len(sink.events) != 0 {
		t.Fatalf("publish with an unreadable unit source: err=%v events=%d, want an error and nothing signed", err, len(sink.events))
	}
}

type failingUnitSource struct{ ProjectionSource }

func (failingUnitSource) ListEnvironmentDeploymentUnits(context.Context, uuid.UUID) ([]domain.DeploymentUnit, error) {
	return nil, repository.ErrConflict
}

func assertRecordUnits(t *testing.T, got, stored []domain.DeploymentUnit, wantKeys ...string) {
	t.Helper()
	if len(got) != len(wantKeys) {
		t.Fatalf("record units = %+v, want keys %v", got, wantKeys)
	}
	for i, key := range wantKeys {
		unit := got[i]
		if unit.Key != key || unit.Implicit {
			t.Fatalf("record unit %d = %+v, want explicit %q (sorted by key)", i, unit, key)
		}
		if unit.EnvironmentID != uuid.Nil {
			t.Fatalf("record unit %q carries environment_id: %+v", key, unit)
		}
		want := unitByKey(stored, key)
		want.EnvironmentID = uuid.Nil
		gotJSON, _ := json.Marshal(unit)
		wantJSON, _ := json.Marshal(want)
		if string(gotJSON) != string(wantJSON) {
			t.Fatalf("record unit %q does not round-trip the stored unit:\n got %s\nwant %s", key, gotJSON, wantJSON)
		}
	}
}

func unitByKey(units []domain.DeploymentUnit, key string) domain.DeploymentUnit {
	for _, unit := range units {
		if unit.Key == key {
			return unit
		}
	}
	return domain.DeploymentUnit{}
}

func (r *relayFirstTestServiceRepo) GetByIDForUpdate(ctx context.Context, id uuid.UUID) (*domain.Service, error) {
	return r.GetByID(ctx, id)
}

func (r *relayFirstTestEnvironmentRepo) GetByIDForUpdate(ctx context.Context, id uuid.UUID) (*domain.Environment, error) {
	return r.GetByID(ctx, id)
}

// relayFirstTestTx hands the cache repositories to a transaction (no
// rollback: the tests only commit).
type relayFirstTestTx struct {
	services     *relayFirstTestServiceRepo
	environments *relayFirstTestEnvironmentRepo
	units        *relayFirstTestUnitRepo
}

func (tx relayFirstTestTx) WithinTx(_ context.Context, fn func(repository.TxRepos) error) error {
	return fn(repository.TxRepos{Services: tx.services, Environments: tx.environments, DeploymentUnits: tx.units})
}

// relayFirstTestUnitRepo stores explicit units like PgDeploymentUnitRepository:
// supplied ids are kept and timestamps are stamped by the store.
type relayFirstTestUnitRepo struct {
	mu   sync.Mutex
	rows map[uuid.UUID]domain.DeploymentUnit
}

func (r *relayFirstTestUnitRepo) Create(_ context.Context, unit *domain.DeploymentUnit) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if unit.ID == uuid.Nil {
		unit.ID = domain.NewEntityID()
	}
	domain.NormalizeDeploymentUnitTargeting(unit)
	domain.StampCreateRevision(&unit.CreatedAt, &unit.UpdatedAt)
	unit.Implicit = false
	r.rows[unit.ID] = *unit
	return nil
}

func (r *relayFirstTestUnitRepo) Update(_ context.Context, unit *domain.DeploymentUnit) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.rows[unit.ID]; !ok {
		return repository.ErrNotFound
	}
	domain.NormalizeDeploymentUnitTargeting(unit)
	unit.UpdatedAt, unit.Implicit = domain.NewRevisionTime(), false
	r.rows[unit.ID] = *unit
	return nil
}

func (r *relayFirstTestUnitRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentUnit, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	unit, ok := r.rows[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return &unit, nil
}

func (r *relayFirstTestUnitRepo) GetByEnvironmentKey(ctx context.Context, environmentID uuid.UUID, key string) (*domain.DeploymentUnit, error) {
	units, _ := r.ListByEnvironment(ctx, environmentID)
	for _, unit := range units {
		if unit.Key == key {
			return &unit, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (r *relayFirstTestUnitRepo) ListByEnvironment(_ context.Context, environmentID uuid.UUID) ([]domain.DeploymentUnit, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.DeploymentUnit
	for _, unit := range r.rows {
		if unit.EnvironmentID == environmentID {
			out = append(out, unit)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (r *relayFirstTestUnitRepo) ListByEnvironmentForUpdate(ctx context.Context, environmentID uuid.UUID) ([]domain.DeploymentUnit, error) {
	return r.ListByEnvironment(ctx, environmentID)
}

func (r *relayFirstTestUnitRepo) ResolveDefault(context.Context, *domain.Environment) (*domain.DeploymentUnit, error) {
	return nil, repository.ErrNotFound
}

func (r *relayFirstTestUnitRepo) DeleteIfUnreferenced(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.rows, id)
	return nil
}
