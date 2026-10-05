package service

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

const supervisionTestKey = "f555555555555555555555555555555555555555555555555555555555555555"

// supervisionFixture is a daemon's local event store and service key. Records
// are saved into the real store the way inbound relay sync and the daemon's
// own publisher save them, so supervisors read what production reads.
type supervisionFixture struct {
	t      *testing.T
	store  *localstore.Store
	secret gonostr.SecretKey
	state  LocalSupervisionState
}

func newSupervisionFixture(t *testing.T) *supervisionFixture {
	t.Helper()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "events.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	secret, err := gonostr.SecretKeyFromHex(supervisionTestKey)
	require.NoError(t, err)
	state, err := NewLocalSupervisionState(store, secret.Public().Hex())
	require.NoError(t, err)
	return &supervisionFixture{t: t, store: store, secret: secret, state: state}
}

// deliver saves one signed cp-state record of a projected family, as a relay
// event reaching the local store does. The envelope tags are those of the
// projector's controlStateEnvelope.
func (f *supervisionFixture) deliver(legacyKind int, domainName, topic, d string, deleted bool, content map[string]any, createdAt time.Time) {
	f.t.Helper()
	content["deleted"] = deleted
	encoded, err := json.Marshal(content)
	require.NoError(f.t, err)
	event := gonostr.Event{Kind: gonostr.Kind(kinds.CASControlState), CreatedAt: gonostr.Timestamp(createdAt.Unix()), Content: string(encoded), Tags: gonostr.Tags{
		{kinds.CASControlStateTagD, d},
		{kinds.CASControlStateTagDomain, domainName},
		{kinds.CASControlStateTagSchema, kinds.CASControlStateSchema},
		{kinds.CASControlStateTagLegacyKind, strconv.Itoa(legacyKind)},
		{kinds.CASControlStateTagDeleted, strconv.FormatBool(deleted)},
		{"t", topic},
	}}
	require.NoError(f.t, event.Sign(f.secret))
	_, err = f.store.SaveEvent(event)
	require.NoError(f.t, err)
}

func serviceStateCoordinate(serviceID, environmentID uuid.UUID) string {
	return "service:" + serviceID.String() + ":environment:" + environmentID.String()
}

// deliverServiceState saves a service-state record in the wire shape of the
// relay-first state publisher (RuntimeStateRecord).
func (f *supervisionFixture) deliverServiceState(state domain.EnvironmentServiceState, createdAt time.Time) {
	f.t.Helper()
	content := map[string]any{
		"service_id":     state.ServiceID.String(),
		"environment_id": state.EnvironmentID.String(),
		"drift_status":   string(state.DriftStatus),
		"updated_at":     createdAt.UTC().Format(time.RFC3339Nano),
	}
	if state.DeploymentUnitID != nil {
		content["deployment_unit_id"] = state.DeploymentUnitID.String()
	}
	if state.DesiredRuntimeState != nil {
		content["desired_runtime_state"] = state.DesiredRuntimeState
	}
	if state.DesiredArtifactID != nil {
		content["desired_artifact_id"] = state.DesiredArtifactID.String()
	}
	f.deliver(kinds.ServiceState, "service", kinds.CPStateTopicServiceState, serviceStateCoordinate(state.ServiceID, state.EnvironmentID), false, content, createdAt)
}

func (f *supervisionFixture) deliverServiceStateTombstone(serviceID, environmentID uuid.UUID, createdAt time.Time) {
	f.t.Helper()
	content := map[string]any{"service_id": serviceID.String(), "environment_id": environmentID.String()}
	f.deliver(kinds.ServiceState, "service", kinds.CPStateTopicServiceState, serviceStateCoordinate(serviceID, environmentID), true, content, createdAt)
}

func (f *supervisionFixture) deliverService(svc domain.Service, createdAt time.Time) {
	f.t.Helper()
	content := map[string]any{"id": svc.ID.String(), "name": svc.Name, "repo_url": svc.RepoURL, "artifact_repo": svc.ArtifactRepo, "default_branch": svc.DefaultBranch, "runtime_type": string(svc.RuntimeType)}
	if svc.RuntimeConfig != nil {
		content["runtime_config"] = svc.RuntimeConfig
	}
	f.deliver(kinds.ServiceRegistry, "service", kinds.CPStateTopicServiceRegistry, svc.ID.String(), false, content, createdAt)
}

func (f *supervisionFixture) deliverServiceTombstone(serviceID uuid.UUID, createdAt time.Time) {
	f.t.Helper()
	f.deliver(kinds.ServiceRegistry, "service", kinds.CPStateTopicServiceRegistry, serviceID.String(), true, map[string]any{"id": serviceID.String()}, createdAt)
}

// deliverEnvironment saves an environment-registry record carrying units, or
// the implicit default unit when there are none.
func (f *supervisionFixture) deliverEnvironment(env domain.Environment, units []map[string]any, createdAt time.Time) {
	f.t.Helper()
	if len(units) == 0 {
		units = []map[string]any{{"key": domain.DefaultDeploymentUnitKey, "implicit": true}}
	}
	content := map[string]any{"id": env.ID.String(), "name": env.Name, "loom_worker_selector": map[string]any{}, "runtime_config": env.RuntimeConfig, "protected": env.Protected, "deploy_strategy": string(env.DeployStrategy), "targeting": env.Targeting, "deployment_units": units}
	f.deliver(kinds.EnvironmentRegistry, "environment", kinds.CPStateTopicEnvironmentRegistry, env.ID.String(), false, content, createdAt)
}

// stored returns the daemon's stored events of kind carrying the schema tag.
func (f *supervisionFixture) stored(kind int, schema string) []gonostr.Event {
	f.t.Helper()
	var out []gonostr.Event
	for ev := range f.store.QueryEvents(gonostr.Filter{Kinds: []gonostr.Kind{gonostr.Kind(kind)}, Authors: []gonostr.PubKey{f.secret.Public()}}) {
		if supervisionTag(ev, kinds.CASControlStateTagSchema) == schema {
			out = append(out, ev)
		}
	}
	return out
}

// storePublisher is the daemon's relay publisher as supervision sees it: it
// signs an event with the service key and keeps it in the local event store,
// as Publisher.keepOwnEvent does before relay delivery.
type storePublisher struct {
	store  *localstore.Store
	secret gonostr.SecretKey

	mu     sync.Mutex
	failed error
	events []gonostr.Event
}

func (f *supervisionFixture) publisher() *storePublisher {
	return &storePublisher{store: f.store, secret: f.secret}
}

func (p *storePublisher) PublishSignedEvent(_ context.Context, event *gonostr.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failed != nil {
		return p.failed
	}
	if err := event.Sign(p.secret); err != nil {
		return err
	}
	if _, err := p.store.SaveEvent(*event); err != nil {
		return err
	}
	p.events = append(p.events, *event)
	return nil
}

// failWith makes every publish fail with err (nil restores publishing).
func (p *storePublisher) failWith(err error) {
	p.mu.Lock()
	p.failed = err
	p.mu.Unlock()
}

// published returns how many events of kind with schema were published.
func (p *storePublisher) published(kind int, schema string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	count := 0
	for _, ev := range p.events {
		if int(ev.Kind) == kind && supervisionTag(ev, kinds.CASControlStateTagSchema) == schema {
			count++
		}
	}
	return count
}

func (p *storePublisher) total() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.events)
}

var errSQLOffline = errors.New("SQL offline")

// testClock is an injected supervisor clock.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// readinessGate is a SupervisionReadiness the test opens.
type readinessGate struct{ ch chan struct{} }

func newReadinessGate() *readinessGate                { return &readinessGate{ch: make(chan struct{})} }
func (g *readinessGate) ReadySignal() <-chan struct{} { return g.ch }
func (g *readinessGate) open()                        { close(g.ch) }
