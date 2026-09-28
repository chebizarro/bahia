package nostr

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type fakeDeploymentInventorySource struct {
	artifacts  map[uuid.UUID]domain.Artifact
	units      map[uuid.UUID]domain.DeploymentUnit
	instances  []domain.ManagedInstanceHealth
	supervised bool
}

func (s *fakeDeploymentInventorySource) GetArtifact(_ context.Context, id uuid.UUID) (*domain.Artifact, error) {
	artifact, ok := s.artifacts[id]
	if !ok {
		return nil, nil
	}
	return &artifact, nil
}

func (s *fakeDeploymentInventorySource) GetDeploymentUnit(_ context.Context, id uuid.UUID) (*domain.DeploymentUnit, error) {
	unit, ok := s.units[id]
	if !ok {
		return nil, nil
	}
	return &unit, nil
}

func (s *fakeDeploymentInventorySource) ListManagedInstances(context.Context) ([]domain.ManagedInstanceHealth, bool, error) {
	return s.instances, s.supervised, nil
}

func digestOf(ch string) string { return "sha256:" + strings.Repeat(ch, 64) }

func deploymentInventoryEvents(sink *captureProjectionPublisher, entity string) []gonostr.Event {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var out []gonostr.Event
	for _, ev := range sink.events {
		if int(ev.Kind) == KindCASControlState && hasTag(ev.Tags, "domain", DeploymentInventoryDomain) && hasTag(ev.Tags, "entity", entity) {
			out = append(out, ev)
		}
	}
	return out
}

func latestInventoryByD(t *testing.T, events []gonostr.Event) map[string]gonostr.Event {
	t.Helper()
	out := map[string]gonostr.Event{}
	for _, ev := range events {
		out[eventDTag(ev)] = ev
	}
	return out
}

func decodeEnvironmentInventory(t *testing.T, ev gonostr.Event) deploymentInventoryEnvironmentPayload {
	t.Helper()
	if !ev.VerifySignature() {
		t.Fatalf("inventory event %s has an invalid signature", eventDTag(ev))
	}
	var payload deploymentInventoryEnvironmentPayload
	if err := json.Unmarshal([]byte(ev.Content), &payload); err != nil {
		t.Fatalf("decode inventory: %v", err)
	}
	return payload
}

func rowByService(t *testing.T, payload deploymentInventoryEnvironmentPayload, name string) deploymentInventoryDeployment {
	t.Helper()
	for _, row := range payload.Deployments {
		if row.Service.Name == name {
			return row
		}
	}
	t.Fatalf("inventory for %s has no %s row: %#v", payload.Environment.Name, name, payload.Deployments)
	return deploymentInventoryDeployment{}
}

type inventoryFixture struct {
	source    *fakeProjectionSource
	inventory *fakeDeploymentInventorySource
	prodID    uuid.UUID
	stagingID uuid.UUID
	ids       map[string]uuid.UUID
	now       time.Time
}

// newInventoryFixture models production with bahia (two supervised
// instances), a stopped relay, a desired-only web app, an observed-only
// adopted workload, an unhealthy worker whose state lags its latest
// observation, and a staging copy of bahia that must stay a distinct row.
func newInventoryFixture() *inventoryFixture {
	f := &inventoryFixture{
		source:    newFakeProjectionSource(),
		inventory: &fakeDeploymentInventorySource{artifacts: map[uuid.UUID]domain.Artifact{}, units: map[uuid.UUID]domain.DeploymentUnit{}, supervised: true},
		prodID:    uuid.New(),
		stagingID: uuid.New(),
		ids:       map[string]uuid.UUID{},
		now:       time.Now().UTC().Truncate(time.Second),
	}
	f.source.envs[f.prodID] = domain.Environment{ID: f.prodID, Name: "production"}
	f.source.envs[f.stagingID] = domain.Environment{ID: f.stagingID, Name: "staging"}
	for _, name := range []string{"bahia", "bahia-relay", "bahia-web", "legacy-observer", "worker"} {
		id := uuid.New()
		f.ids[name] = id
		f.source.services[id] = domain.Service{ID: id, Name: name, RuntimeType: domain.RuntimeTypeCompose}
	}
	unitID := uuid.New()
	f.inventory.units[unitID] = domain.DeploymentUnit{ID: unitID, EnvironmentID: f.prodID, Key: "edge-01", DisplayName: "Edge 01", EndpointRef: "edge-01-docker", OwnershipMode: domain.OwnershipModeBahiaManaged, ReconcileMode: domain.ReconcileModeAutoApply}

	artifact := func(name, ch string) uuid.UUID {
		id := uuid.New()
		f.inventory.artifacts[id] = domain.Artifact{ID: id, ImageRepo: "ghcr.io/openagentsinc/" + name, ImageTag: "v1.2.3", ImageDigest: digestOf(ch)}
		return id
	}
	observe := func(service, env uuid.UUID, ch string, health domain.HealthStatus, at time.Time) uuid.UUID {
		id := uuid.New()
		f.source.observations[id] = domain.RuntimeObservation{ID: id, ServiceID: service, EnvironmentID: env, ObservedImageRepo: "ghcr.io/openagentsinc/x", ObservedImageDigest: digestOf(ch), ObservedVersion: "1.2.3", HealthStatus: health, Source: "compose", ObservedAt: at}
		return id
	}

	bahiaArtifact := artifact("bahia", "a")
	bahiaObs := observe(f.ids["bahia"], f.prodID, "a", domain.HealthStatusHealthy, f.now)
	f.source.states[stateKeyForTest(f.ids["bahia"], f.prodID)] = domain.EnvironmentServiceState{ServiceID: f.ids["bahia"], EnvironmentID: f.prodID, DeploymentUnitID: &unitID, DesiredArtifactID: &bahiaArtifact, CurrentObservationID: &bahiaObs, DriftStatus: domain.DriftStatusInSync}

	relayArtifact := artifact("bahia-relay", "c")
	relayObs := observe(f.ids["bahia-relay"], f.prodID, "c", domain.HealthStatusStopped, f.now)
	f.source.states[stateKeyForTest(f.ids["bahia-relay"], f.prodID)] = domain.EnvironmentServiceState{ServiceID: f.ids["bahia-relay"], EnvironmentID: f.prodID, DesiredArtifactID: &relayArtifact, CurrentObservationID: &relayObs, DriftStatus: domain.DriftStatusDrifted}

	webArtifact := artifact("bahia-web", "d")
	f.source.states[stateKeyForTest(f.ids["bahia-web"], f.prodID)] = domain.EnvironmentServiceState{ServiceID: f.ids["bahia-web"], EnvironmentID: f.prodID, DesiredArtifactID: &webArtifact, DriftStatus: domain.DriftStatusUnknown}

	legacyObs := observe(f.ids["legacy-observer"], f.prodID, "e", domain.HealthStatusHealthy, f.now)
	f.source.states[stateKeyForTest(f.ids["legacy-observer"], f.prodID)] = domain.EnvironmentServiceState{ServiceID: f.ids["legacy-observer"], EnvironmentID: f.prodID, CurrentObservationID: &legacyObs, DriftStatus: domain.DriftStatusUnknown}

	workerArtifact := artifact("worker", "f")
	staleObs := observe(f.ids["worker"], f.prodID, "f", domain.HealthStatusHealthy, f.now.Add(-2*time.Hour))
	observe(f.ids["worker"], f.prodID, "9", domain.HealthStatusUnhealthy, f.now.Add(-time.Hour))
	f.source.states[stateKeyForTest(f.ids["worker"], f.prodID)] = domain.EnvironmentServiceState{ServiceID: f.ids["worker"], EnvironmentID: f.prodID, DesiredArtifactID: &workerArtifact, CurrentObservationID: &staleObs, DriftStatus: domain.DriftStatusInSync,
		ReconcileConsecutiveFailures: 2, ReconcileFailureMetadata: map[string]any{"reason": "auto_apply_failed", "message": "dial tcp://10.9.8.7:2376: password=hunter2"}}

	stagingArtifact := artifact("bahia", "b")
	stagingObs := observe(f.ids["bahia"], f.stagingID, "b", domain.HealthStatusHealthy, f.now)
	f.source.states[stateKeyForTest(f.ids["bahia"], f.stagingID)] = domain.EnvironmentServiceState{ServiceID: f.ids["bahia"], EnvironmentID: f.stagingID, DesiredArtifactID: &stagingArtifact, CurrentObservationID: &stagingObs, DriftStatus: domain.DriftStatusInSync}

	for _, target := range []string{"bahia-1", "bahia-2"} {
		f.inventory.instances = append(f.inventory.instances, domain.ManagedInstanceHealth{
			ManagedInstanceKey: domain.ManagedInstanceKey{ServiceID: f.ids["bahia"], EnvironmentID: f.prodID, DeploymentUnitID: unitID, RuntimeTargetName: target},
			Host:               "tcp://10.1.2.3:2376", SupervisorType: domain.InstanceSupervisorCompose, Status: domain.InstanceHealthStatusHealthy, LastObservedAt: f.now,
		})
	}
	f.inventory.instances[1].Status = domain.InstanceHealthStatusRestartLoop
	return f
}

func newInventoryProjector(f *inventoryFixture, sink *captureProjectionPublisher, opts ...ProjectorOption) *Projector {
	cfg := config.Defaults()
	cfg.Nostr.PrivateKey = projectorTestPrivateKey
	cfg.Nostr.PublishEnabled = true
	base := []ProjectorOption{WithSystemDiscoveryConfig(cfg, true), WithDeploymentInventorySource(f.inventory)}
	return NewProjector(cfg.Nostr, f.source, sink, nil, zap.NewNop(), append(base, opts...)...)
}

func TestDeploymentInventoryPublishesCompleteSignedEnvironmentSnapshots(t *testing.T) {
	f := newInventoryFixture()
	sink := &captureProjectionPublisher{}
	projector := newInventoryProjector(f, sink)
	if err := projector.RepublishSnapshot(context.Background()); err != nil {
		t.Fatalf("republish snapshot: %v", err)
	}

	byD := latestInventoryByD(t, deploymentInventoryEvents(sink, DeploymentInventoryEnvironmentEntity))
	if len(byD) != 2 {
		t.Fatalf("published %d environment inventories, want production and staging", len(byD))
	}
	prodEvent, ok := byD[deploymentInventoryEnvironmentDTag(f.prodID)]
	if !ok {
		t.Fatalf("missing production coordinate; got %v", byD)
	}
	for _, tag := range [][2]string{{"domain", DeploymentInventoryDomain}, {"schema", DeploymentInventorySchema}, {"entity", DeploymentInventoryEnvironmentEntity}, {"environment", f.prodID.String()}, {"status", "attention"}, {"deleted", "false"}} {
		if !hasTag(prodEvent.Tags, tag[0], tag[1]) {
			t.Fatalf("production inventory lacks tag %v: %v", tag, prodEvent.Tags)
		}
	}
	prod := decodeEnvironmentInventory(t, prodEvent)
	if !prod.Complete || prod.Schema != DeploymentInventorySchema || prod.Environment.Name != "production" || prod.InstanceCoverage != instanceCoverageSupervised {
		t.Fatalf("production envelope = %+v", prod)
	}
	wantStale := int64((10*time.Minute + 2*config.Defaults().Reconcile.Interval) / time.Second)
	if prod.Freshness.StaleAfterSeconds != wantStale {
		t.Fatalf("stale_after_seconds = %d, want %d", prod.Freshness.StaleAfterSeconds, wantStale)
	}
	if len(prod.Deployments) != 5 {
		t.Fatalf("production deployments = %d, want every Bahia-known deployment including desired-only and observed-only", len(prod.Deployments))
	}

	bahia := rowByService(t, prod, "bahia")
	if bahia.Coverage != DeploymentCoverageObserved || bahia.DriftStatus != "in_sync" || !bahia.DriftEvaluated {
		t.Fatalf("bahia row = %+v", bahia)
	}
	if bahia.Desired == nil || bahia.Desired.ImageRef != "ghcr.io/openagentsinc/bahia@"+digestOf("a") || !bahia.Desired.Immutable || bahia.Desired.ImageTag != "v1.2.3" {
		t.Fatalf("bahia desired = %+v, want immutable artifact reference", bahia.Desired)
	}
	if bahia.Observed == nil || bahia.Observed.ImageDigest != digestOf("a") || bahia.Observed.Health != "healthy" || bahia.Observed.Source != "compose" || bahia.Observed.ObservedAt == "" {
		t.Fatalf("bahia observed = %+v", bahia.Observed)
	}
	if bahia.DeploymentUnit.Key != "edge-01" || bahia.DeploymentUnit.Target != "edge-01-docker" || bahia.DeploymentUnit.OwnershipMode != "bahia_managed" || bahia.DeploymentUnit.Implicit {
		t.Fatalf("bahia unit = %+v", bahia.DeploymentUnit)
	}
	if len(bahia.Instances) != 2 || bahia.Instances[0].Target != "bahia-1" || bahia.Instances[1].Target != "bahia-2" || bahia.Instances[1].Status != "restart_loop" {
		t.Fatalf("bahia instances = %+v, want two distinct supervised instances", bahia.Instances)
	}

	relay := rowByService(t, prod, "bahia-relay")
	if relay.Coverage != DeploymentCoverageObserved || relay.Observed == nil || relay.Observed.Health != "stopped" || relay.DriftStatus != "drifted" {
		t.Fatalf("stopped relay row = %+v", relay)
	}
	if !relay.DeploymentUnit.Implicit || relay.DeploymentUnit.Key != domain.DefaultDeploymentUnitKey || len(relay.Instances) != 0 {
		t.Fatalf("relay unit/instances = %+v / %+v", relay.DeploymentUnit, relay.Instances)
	}
	if web := rowByService(t, prod, "bahia-web"); web.Coverage != DeploymentCoverageDesiredOnly || web.Observed != nil || web.Desired == nil {
		t.Fatalf("desired-only web row = %+v", web)
	}
	if legacy := rowByService(t, prod, "legacy-observer"); legacy.Coverage != DeploymentCoverageObservedOnly || legacy.Desired != nil || legacy.Observed == nil {
		t.Fatalf("observed-only row = %+v", legacy)
	}
	worker := rowByService(t, prod, "worker")
	if worker.Observed == nil || worker.Observed.Health != "unhealthy" || worker.Observed.ImageDigest != digestOf("9") || worker.DriftEvaluated {
		t.Fatalf("worker must expose its latest unhealthy observation with drift not yet evaluated: %+v", worker)
	}
	if worker.Reconcile == nil || worker.Reconcile.FailureReason != "auto_apply_failed" || worker.Reconcile.ConsecutiveFailures != 2 {
		t.Fatalf("worker reconcile = %+v", worker.Reconcile)
	}
	if prod.Summary[DeploymentCoverageObserved] != 3 || prod.Summary[DeploymentCoverageDesiredOnly] != 1 || prod.Summary[DeploymentCoverageObservedOnly] != 1 {
		t.Fatalf("summary = %+v", prod.Summary)
	}

	staging := decodeEnvironmentInventory(t, byD[deploymentInventoryEnvironmentDTag(f.stagingID)])
	stagingBahia := rowByService(t, staging, "bahia")
	if stagingBahia.Observed == nil || stagingBahia.Observed.ImageDigest != digestOf("b") || len(staging.Deployments) != 1 {
		t.Fatalf("staging bahia must remain a distinct deployment with its own version: %+v", staging.Deployments)
	}
}

func TestDeploymentInventoryReportsUnsupervisedInstanceCoverage(t *testing.T) {
	f := newInventoryFixture()
	f.inventory.supervised = false
	sink := &captureProjectionPublisher{}
	if err := newInventoryProjector(f, sink).RepublishSnapshot(context.Background()); err != nil {
		t.Fatalf("republish snapshot: %v", err)
	}
	prod := decodeEnvironmentInventory(t, latestInventoryByD(t, deploymentInventoryEvents(sink, DeploymentInventoryEnvironmentEntity))[deploymentInventoryEnvironmentDTag(f.prodID)])
	if prod.InstanceCoverage != instanceCoverageNotSupervised || len(rowByService(t, prod, "bahia").Instances) != 0 {
		t.Fatalf("unsupervised instance coverage = %q, instances = %+v", prod.InstanceCoverage, rowByService(t, prod, "bahia").Instances)
	}
}

func TestDeploymentInventoryRedactsRuntimeDetailFromEveryPublicEvent(t *testing.T) {
	f := newInventoryFixture()
	bahiaState := f.source.states[stateKeyForTest(f.ids["bahia"], f.prodID)]
	obs := f.source.observations[*bahiaState.CurrentObservationID]
	obs.ObservedHost = "tcp://10.0.0.5:2376"
	obs.ObservedContainerID = "c0ffee-container-id"
	obs.ObservedVersion = "1.2.3 --token=abc"
	obs.Metadata = map[string]any{"docker_status": "Up 3 hours", "env": "DATABASE_URL=postgres://admin:s3cret@db/bahia"}
	obs.NormalizedState = &domain.NormalizedObservation{
		ImageRef: "ghcr.io/openagentsinc/bahia:v1", Command: []string{"serve", "--api-key=sk-live-123"},
		Env: map[string]string{"DATABASE_URL": "postgres://admin:s3cret@db/bahia"}, Volumes: []string{"/srv/secrets:/run/secrets"},
	}
	f.source.observations[obs.ID] = obs
	bahiaState.DesiredRuntimeState = &domain.DesiredServiceSpec{ImageRef: "ghcr.io/openagentsinc/bahia@" + digestOf("a"), Env: map[string]string{"NSEC": "nsec1secretsecret"}, Command: []string{"--password=hunter2"}}
	f.source.states[stateKeyForTest(f.ids["bahia"], f.prodID)] = bahiaState

	sink := &captureProjectionPublisher{}
	projector := newInventoryProjector(f, sink)
	ctx := context.Background()
	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish snapshot: %v", err)
	}
	// The generic audit path projects runtime observation events publicly too.
	projector.handleEvent(ctx, events.Event{Type: events.EventRuntimeObservation, EntityID: obs.ID.String(), Data: &obs})

	forbidden := []string{"10.0.0.5", "10.1.2.3", "10.9.8.7", "c0ffee-container-id", "--token", "s3cret", "sk-live-123", "/srv/secrets", "nsec1secretsecret", "hunter2", "DATABASE_URL", "docker_status"}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.events) == 0 {
		t.Fatal("expected published events")
	}
	sawAudit := false
	for _, ev := range sink.events {
		encoded, _ := json.Marshal(struct {
			Tags    gonostr.Tags
			Content string
		}{ev.Tags, ev.Content})
		for _, secret := range forbidden {
			if strings.Contains(string(encoded), secret) {
				t.Fatalf("kind %d event %s leaked %q: %s", ev.Kind, eventDTag(ev), secret, encoded)
			}
		}
		if int(ev.Kind) == KindCASAudit && hasTag(ev.Tags, "event_type", string(events.EventRuntimeObservation)) {
			sawAudit = true
			if !strings.Contains(ev.Content, digestOf("a")) {
				t.Fatalf("observation audit lost its allowlisted digest: %s", ev.Content)
			}
		}
	}
	if !sawAudit {
		t.Fatal("expected a runtime observation audit event")
	}
}

func TestDeploymentInventoryTombstonesDeletedEnvironmentsAcrossRestart(t *testing.T) {
	f := newInventoryFixture()
	repo := newMemoryNostrEventRepo()
	cfg := config.Defaults()
	cfg.Nostr.PrivateKey = projectorTestPrivateKey
	cfg.Nostr.PublishEnabled = true
	ctx := context.Background()

	first := NewProjector(cfg.Nostr, f.source, &captureProjectionPublisher{}, repo, zap.NewNop(), WithDeploymentInventorySource(f.inventory))
	if _, _, err := first.publishDeploymentInventorySnapshot(ctx); err != nil {
		t.Fatalf("initial inventory: %v", err)
	}

	// Staging is deleted while the process is down; the restarted projector
	// rebuilds its published set from its own retained events.
	delete(f.source.envs, f.stagingID)
	sink := &captureProjectionPublisher{}
	restarted := NewProjector(cfg.Nostr, f.source, sink, repo, zap.NewNop(), WithDeploymentInventorySource(f.inventory))
	published, tombstones, err := restarted.publishDeploymentInventorySnapshot(ctx)
	if err != nil {
		t.Fatalf("restart inventory: %v", err)
	}
	if tombstones != 1 || published != 1 {
		t.Fatalf("published=%d tombstones=%d, want 1/1", published, tombstones)
	}
	var tombstone *gonostr.Event
	for _, ev := range deploymentInventoryEvents(sink, DeploymentInventoryEnvironmentEntity) {
		if eventDTag(ev) == deploymentInventoryEnvironmentDTag(f.stagingID) {
			ev := ev
			tombstone = &ev
		}
	}
	if tombstone == nil || !hasTag(tombstone.Tags, "deleted", "true") || !tombstone.VerifySignature() {
		t.Fatalf("missing signed staging tombstone: %+v", tombstone)
	}
	var content map[string]any
	if err := json.Unmarshal([]byte(tombstone.Content), &content); err != nil || content["deleted"] != true {
		t.Fatalf("tombstone content = %s", tombstone.Content)
	}

	// A further pass must not re-tombstone the same coordinate.
	again := &captureProjectionPublisher{}
	restarted.publisher = again
	if _, tombstones, err := restarted.publishDeploymentInventorySnapshot(ctx); err != nil || tombstones != 0 {
		t.Fatalf("second pass tombstones=%d err=%v", tombstones, err)
	}
}

func TestDeploymentInventoryRepublishesOnRollbackAndSuppressesUnchanged(t *testing.T) {
	f := newInventoryFixture()
	sink := &captureProjectionPublisher{}
	projector := newInventoryProjector(f, sink)
	ctx := context.Background()
	if _, _, err := projector.publishDeploymentInventorySnapshot(ctx); err != nil {
		t.Fatalf("initial inventory: %v", err)
	}
	before := len(deploymentInventoryEvents(sink, DeploymentInventoryEnvironmentEntity))

	// No material change: the dedupe cache suppresses identical snapshots.
	projector.handleEvent(ctx, events.Event{Type: events.EventEnvironmentServiceStateChanged, EntityID: "noop"})
	if after := len(deploymentInventoryEvents(sink, DeploymentInventoryEnvironmentEntity)); after != before {
		t.Fatalf("unchanged inventory republished: %d -> %d", before, after)
	}

	// Rollback: runtime now observes the previous digest.
	rollbackID := uuid.New()
	f.source.observations[rollbackID] = domain.RuntimeObservation{ID: rollbackID, ServiceID: f.ids["bahia"], EnvironmentID: f.prodID, ObservedImageDigest: digestOf("7"), HealthStatus: domain.HealthStatusHealthy, Source: "compose", ObservedAt: f.now.Add(time.Minute)}
	state := f.source.states[stateKeyForTest(f.ids["bahia"], f.prodID)]
	state.CurrentObservationID = &rollbackID
	state.DriftStatus = domain.DriftStatusDrifted
	f.source.states[stateKeyForTest(f.ids["bahia"], f.prodID)] = state
	projector.handleEvent(ctx, events.Event{Type: events.EventRuntimeObservation, EntityID: rollbackID.String()})

	var latest gonostr.Event
	for _, ev := range deploymentInventoryEvents(sink, DeploymentInventoryEnvironmentEntity) {
		if eventDTag(ev) == deploymentInventoryEnvironmentDTag(f.prodID) {
			latest = ev
		}
	}
	bahia := rowByService(t, decodeEnvironmentInventory(t, latest), "bahia")
	if bahia.Observed == nil || bahia.Observed.ImageDigest != digestOf("7") || bahia.DriftStatus != "drifted" {
		t.Fatalf("rollback not reflected in inventory: %+v", bahia)
	}
}

func TestRuntimeTargetScanPublishesOnlyRedactedAggregates(t *testing.T) {
	adoptedID := uuid.New()
	previews := []service.AdoptionPreview{
		{
			Target: service.AdoptionTarget{Name: "edge-01", EnvironmentName: "production", EndpointRef: "edge-01-docker", DockerHost: "tcp://10.0.0.5:2376"},
			Containers: []service.AdoptionPreviewContainer{
				{Discovered: runtime.DiscoveredContainer{ContainerID: "aaaa1111", ContainerName: "mystery-db", ImageRepo: "postgres", ImageDigest: digestOf("1"), TargetName: "mystery-db", Environment: map[string]string{"POSTGRES_PASSWORD": "pw-123"}}},
				{Discovered: runtime.DiscoveredContainer{ContainerID: "bbbb2222", ContainerName: "stray-cache", ImageRepo: "redis", ImageDigest: digestOf("2"), TargetName: "stray-cache"}},
				{Discovered: runtime.DiscoveredContainer{ContainerID: "cccc3333", ContainerName: "bahia", Labels: map[string]string{"bahia.managed": "true"}, TargetName: "bahia"}},
				{Discovered: runtime.DiscoveredContainer{ContainerID: "dddd4444", ContainerName: "adopted-api", TargetName: "adopted-api"}, ExistingServiceID: &adoptedID, WillUpdate: true},
			},
		},
		{Target: service.AdoptionTarget{Name: "edge-02", EnvironmentName: "production", DockerHost: "tcp://10.0.0.6:2376"}, Error: "dial tcp 10.0.0.6:2376: connection refused"},
	}
	scan := service.AdoptionScanCompleted{CompletedAt: time.Now().UTC(), Targets: service.AdoptionScanTargetSummaries(previews)}

	f := newInventoryFixture()
	sink := &captureProjectionPublisher{}
	projector := newInventoryProjector(f, sink)
	projector.handleAdoptionScanCompleted(context.Background(), events.Event{Type: events.EventAdoptionScanCompleted, Data: scan})

	scans := latestInventoryByD(t, deploymentInventoryEvents(sink, DeploymentInventoryTargetScanEntity))
	if len(scans) != 2 {
		t.Fatalf("target scans = %d, want 2", len(scans))
	}
	decode := func(d string) deploymentInventoryTargetScanPayload {
		ev, ok := scans[d]
		if !ok || !ev.VerifySignature() {
			t.Fatalf("missing or unsigned scan %s", d)
		}
		var payload deploymentInventoryTargetScanPayload
		if err := json.Unmarshal([]byte(ev.Content), &payload); err != nil {
			t.Fatalf("decode scan: %v", err)
		}
		return payload
	}
	edge1 := decode(deploymentInventoryTargetScanDTag("production", "edge-01"))
	if edge1.ScanState != targetScanComplete || edge1.Counts == nil || *edge1.Counts != (deploymentInventoryCounts{Total: 4, Managed: 2, Unmanaged: 2}) || edge1.EndpointRef != "edge-01-docker" || edge1.ScannedAt == "" {
		t.Fatalf("edge-01 aggregate = %+v counts=%+v", edge1, edge1.Counts)
	}
	edge2 := decode(deploymentInventoryTargetScanDTag("production", "edge-02"))
	if edge2.ScanState != targetScanUnavailable || edge2.Counts != nil {
		t.Fatalf("unavailable target must not claim counts: %+v", edge2)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, ev := range sink.events {
		encoded, _ := json.Marshal(struct {
			Tags    gonostr.Tags
			Content string
		}{ev.Tags, ev.Content})
		for _, detail := range []string{"aaaa1111", "bbbb2222", "mystery-db", "stray-cache", "postgres", "redis", digestOf("1"), digestOf("2"), "10.0.0.5", "10.0.0.6", "pw-123", "connection refused", "adopted-api"} {
			if strings.Contains(string(encoded), detail) {
				t.Fatalf("public event kind %d leaked per-instance detail %q: %s", ev.Kind, detail, encoded)
			}
		}
		if int(ev.Kind) == KindCASAudit {
			t.Fatalf("adoption scan completion must not produce a public audit: %s", ev.Content)
		}
	}
}

func TestDeploymentInventoryIsNotPublishedWhenProjectorDisabled(t *testing.T) {
	f := newInventoryFixture()
	cfg := config.Defaults()
	cfg.Nostr.PublishEnabled = false
	sink := &captureProjectionPublisher{}
	projector := NewProjector(cfg.Nostr, f.source, sink, nil, zap.NewNop(), WithDeploymentInventorySource(f.inventory))
	if _, _, err := projector.publishDeploymentInventorySnapshot(context.Background()); err != nil {
		t.Fatalf("disabled projector: %v", err)
	}
	projector.handleAdoptionScanCompleted(context.Background(), events.Event{Data: service.AdoptionScanCompleted{Targets: []service.AdoptionScanTargetSummary{{Target: "edge-01", Environment: "production", Available: true}}}})
	if len(sink.byKind(kinds.CASControlState)) != 0 {
		t.Fatal("disabled projector published inventory")
	}
}
