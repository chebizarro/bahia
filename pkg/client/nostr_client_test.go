package client

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// fakeSubscription implements Subscription for tests.
type fakeSubscription struct {
	events            <-chan *nostr.Event
	endOfStoredEvents <-chan struct{}
	relayEOSE         <-chan RelayEOSEInfo
}

func (s *fakeSubscription) Events() <-chan *nostr.Event        { return s.events }
func (s *fakeSubscription) EndOfStoredEvents() <-chan struct{} { return s.endOfStoredEvents }
func (s *fakeSubscription) RelayEOSE() <-chan RelayEOSEInfo    { return s.relayEOSE }
func (s *fakeSubscription) Close()                             {}

// fakePool implements SubscriptionPool for tests. It delivers pre-loaded
// events and fires EOSE after a configurable delay (or not at all).
type fakePool struct {
	events []*nostr.Event
	// eoseDelay controls when EOSE fires. Zero = immediate.
	// Negative = never (simulates timeout).
	eoseDelay time.Duration
	// subscribeErr is returned from SubscribeAllWithEOSE when non-nil.
	subscribeErr error
}

func (f *fakePool) SubscribeAllWithEOSE(_ context.Context, _ []nostr.Filter) (Subscription, error) {
	if f.subscribeErr != nil {
		return nil, f.subscribeErr
	}
	events := make([]*nostr.Event, len(f.events))
	copy(events, f.events)

	evCh := make(chan *nostr.Event, len(events)+1)
	eoseCh := make(chan struct{})
	relayEOSECh := make(chan RelayEOSEInfo, 1)

	go func() {
		for _, ev := range events {
			evCh <- ev
		}
		if f.eoseDelay < 0 {
			// Never fire EOSE — let the caller time out.
			return
		}
		if f.eoseDelay > 0 {
			time.Sleep(f.eoseDelay)
		}
		relayEOSECh <- RelayEOSEInfo{RelayURL: "wss://fake"}
		close(eoseCh)
		// Do not close evCh — in a real relay the event stream stays open
		// after EOSE. Closing it creates a race where select picks the
		// closed channel before the EOSE signal.
	}()

	return &fakeSubscription{
		events:            evCh,
		endOfStoredEvents: eoseCh,
		relayEOSE:         relayEOSECh,
	}, nil
}

// makeServiceEvent builds a signed 30900 service-registry event.
func makeServiceEvent(t *testing.T, sk nostr.SecretKey, svcID uuid.UUID, name string, ts nostr.Timestamp) nostr.Event {
	t.Helper()
	_, envTags := nostrpool.ControlStateEnvelope(kinds.ServiceRegistry, svcID.String(), false)
	content := map[string]any{
		"deleted":        false,
		"id":             svcID.String(),
		"name":           name,
		"artifact_repo":  "ghcr.io/test/" + name,
		"default_branch": "main",
		"runtime_type":   "docker-compose",
		"runtime_config": map[string]any{"adopted": map[string]any{"target_name": name, "source_runtime": "compose", "host_alias": "node-1"}},
		"created_at":     time.Now().UTC().Format(time.RFC3339),
		"updated_at":     time.Now().UTC().Format(time.RFC3339),
	}
	contentJSON, _ := json.Marshal(content)
	ev := nostr.Event{
		Kind:      nostr.Kind(kinds.CASControlState),
		CreatedAt: ts,
		Tags:      envTags,
		Content:   string(contentJSON),
	}
	require.NoError(t, ev.Sign(sk))
	return ev
}

// makeEnvironmentEvent builds a signed 30900 environment-registry event.
func makeEnvironmentEvent(t *testing.T, sk nostr.SecretKey, envID uuid.UUID, name string, ts nostr.Timestamp) nostr.Event {
	t.Helper()
	_, envTags := nostrpool.ControlStateEnvelope(kinds.EnvironmentRegistry, envID.String(), false)
	content := map[string]any{
		"deleted":         false,
		"id":              envID.String(),
		"name":            name,
		"protected":       false,
		"deploy_strategy": "rolling",
		"created_at":      time.Now().UTC().Format(time.RFC3339),
		"updated_at":      time.Now().UTC().Format(time.RFC3339),
	}
	contentJSON, _ := json.Marshal(content)
	ev := nostr.Event{
		Kind:      nostr.Kind(kinds.CASControlState),
		CreatedAt: ts,
		Tags:      envTags,
		Content:   string(contentJSON),
	}
	require.NoError(t, ev.Sign(sk))
	return ev
}

// ---------------------------------------------------------------------------
// Tests: design acceptance (a), (b), (c)
// ---------------------------------------------------------------------------

// TestSyncAndQueryReturnsServiceList is acceptance test (a):
// subscribe → EOSE → query returns service list.
func TestSyncAndQueryReturnsServiceList(t *testing.T) {
	sk := nostr.Generate()
	pubkey := nostr.GetPublicKey(sk)

	svc1ID := uuid.New()
	svc2ID := uuid.New()
	ev1 := makeServiceEvent(t, sk, svc1ID, "api-gateway", nostr.Timestamp(time.Now().Unix()))
	ev2 := makeServiceEvent(t, sk, svc2ID, "auth-service", nostr.Timestamp(time.Now().Unix()+1))

	pool := &fakePool{events: []*nostr.Event{&ev1, &ev2}}

	storePath := filepath.Join(t.TempDir(), "client.bolt")
	c, err := NewNostrClient(NostrClientConfig{
		StorePath:     storePath,
		ServicePubkey: pubkey.Hex(),
		Pool:          pool,
		EOSETimeout:   2 * time.Second,
	})
	require.NoError(t, err)
	defer c.Close()

	events, result, err := c.SyncAndQuery(context.Background(), "service")
	require.NoError(t, err)
	assert.True(t, result.Fresh, "expected fresh after EOSE")

	// Decode services from events.
	var services []string
	for _, ev := range events {
		svc, err := DecodeService(ev)
		if err != nil || svc == nil {
			continue
		}
		services = append(services, svc.Name)
	}
	assert.Contains(t, services, "api-gateway")
	assert.Contains(t, services, "auth-service")
	assert.Len(t, services, 2)
}

// TestCursorPersistence is acceptance test (b):
// cursor persistence — second invocation fetches only new events.
func TestCursorPersistence(t *testing.T) {
	sk := nostr.Generate()
	pubkey := nostr.GetPublicKey(sk)

	svc1ID := uuid.New()
	now := nostr.Timestamp(time.Now().Unix())
	ev1 := makeServiceEvent(t, sk, svc1ID, "first-service", now)

	pool := &fakePool{events: []*nostr.Event{&ev1}}
	storePath := filepath.Join(t.TempDir(), "client.bolt")

	// First sync.
	c, err := NewNostrClient(NostrClientConfig{
		StorePath:     storePath,
		ServicePubkey: pubkey.Hex(),
		Pool:          pool,
		EOSETimeout:   2 * time.Second,
	})
	require.NoError(t, err)

	result, err := c.Sync(context.Background(), "service")
	require.NoError(t, err)
	assert.True(t, result.Fresh)

	events, err := c.QueryDomain("service")
	require.NoError(t, err)
	assert.Len(t, events, 1)

	// Close and reopen with a second event.
	require.NoError(t, c.Close())

	svc2ID := uuid.New()
	ev2 := makeServiceEvent(t, sk, svc2ID, "second-service", now+10)
	pool2 := &fakePool{events: []*nostr.Event{&ev2}}

	c2, err := NewNostrClient(NostrClientConfig{
		StorePath:     storePath,
		ServicePubkey: pubkey.Hex(),
		Pool:          pool2,
		EOSETimeout:   2 * time.Second,
	})
	require.NoError(t, err)
	defer c2.Close()

	// Second sync should only receive ev2 (cursor skips ev1).
	result2, err := c2.Sync(context.Background(), "service")
	require.NoError(t, err)
	assert.True(t, result2.Fresh)

	// But local store should have both events.
	events2, err := c2.QueryDomain("service")
	require.NoError(t, err)
	assert.Len(t, events2, 2)
}

// TestEOSETimeoutServesStaleStore is acceptance test (c):
// EOSE timeout — stale store queried with warning.
func TestEOSETimeoutServesStaleStore(t *testing.T) {
	sk := nostr.Generate()
	pubkey := nostr.GetPublicKey(sk)

	svcID := uuid.New()
	ev := makeServiceEvent(t, sk, svcID, "stale-service", nostr.Timestamp(time.Now().Unix()))

	// Pre-populate the store.
	storePath := filepath.Join(t.TempDir(), "client.bolt")
	store, err := localstore.Open(storePath)
	require.NoError(t, err)
	_, err = store.SaveEvent(ev)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	// Pool that never sends EOSE.
	pool := &fakePool{eoseDelay: -1}

	c, err := NewNostrClient(NostrClientConfig{
		StorePath:     storePath,
		ServicePubkey: pubkey.Hex(),
		Pool:          pool,
		EOSETimeout:   100 * time.Millisecond,
	})
	require.NoError(t, err)
	defer c.Close()

	result, err := c.Sync(context.Background(), "service")
	require.NoError(t, err)
	assert.False(t, result.Fresh, "expected stale when EOSE times out")
	assert.False(t, result.StaleSince.IsZero(), "expected StaleSince to be set")

	// Query should still return the pre-populated event.
	events, err := c.QueryDomain("service")
	require.NoError(t, err)
	assert.Len(t, events, 1)

	svc, err := DecodeService(events[0])
	require.NoError(t, err)
	assert.Equal(t, "stale-service", svc.Name)
}

// TestSubscribeErrorServesStale verifies that when no relays are available,
// the client returns a stale result rather than an error.
func TestSubscribeErrorServesStale(t *testing.T) {
	sk := nostr.Generate()
	pubkey := nostr.GetPublicKey(sk)

	pool := &fakePool{subscribeErr: fmt.Errorf("no relays available")}
	storePath := filepath.Join(t.TempDir(), "client.bolt")

	c, err := NewNostrClient(NostrClientConfig{
		StorePath:     storePath,
		ServicePubkey: pubkey.Hex(),
		Pool:          pool,
		EOSETimeout:   100 * time.Millisecond,
	})
	require.NoError(t, err)
	defer c.Close()

	result, err := c.Sync(context.Background(), "service")
	require.NoError(t, err)
	assert.False(t, result.Fresh)
}

// ---------------------------------------------------------------------------
// Decoder round-trip tests (one per family the design lists)
// ---------------------------------------------------------------------------

func TestDecodeServiceRoundTrip(t *testing.T) {
	sk := nostr.Generate()
	svcID := uuid.New()
	ev := makeServiceEvent(t, sk, svcID, "test-svc", nostr.Timestamp(time.Now().Unix()))

	svc, err := DecodeService(ev)
	require.NoError(t, err)
	require.NotNil(t, svc)
	assert.Equal(t, svcID, svc.ID)
	assert.Equal(t, "test-svc", svc.Name)
	assert.Equal(t, "ghcr.io/test/test-svc", svc.ArtifactRepo)
	assert.Equal(t, "main", svc.DefaultBranch)
	require.NotNil(t, svc.RuntimeConfig)
	require.NotNil(t, svc.RuntimeConfig.Adopted)
	assert.Equal(t, "test-svc", svc.RuntimeConfig.Adopted.TargetName)
}

func TestDecodeEnvironmentRoundTrip(t *testing.T) {
	sk := nostr.Generate()
	envID := uuid.New()
	ev := makeEnvironmentEvent(t, sk, envID, "production", nostr.Timestamp(time.Now().Unix()))

	env, err := DecodeEnvironment(ev)
	require.NoError(t, err)
	require.NotNil(t, env)
	assert.Equal(t, envID, env.ID)
	assert.Equal(t, "production", env.Name)
}

func TestDecodeEnvironmentDetailsRoundTrip(t *testing.T) {
	sk := nostr.Generate()
	envID, unitID := uuid.New(), uuid.New()
	created := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	_, tags := nostrpool.ControlStateEnvelope(kinds.EnvironmentRegistry, envID.String(), false)
	content, err := json.Marshal(map[string]any{
		"id": envID.String(), "name": "production", "protected": true,
		"loom_worker_selector": map[string]any{"region": "west"},
		"runtime_config":       map[string]any{"type": "compose"},
		"targeting":            map[string]any{"default_unit_key": "api", "default_reconcile_mode": "auto_apply"},
		"deploy_strategy":      "canary", "created_at": created.Format(time.RFC3339Nano),
		"updated_at": created.Format(time.RFC3339Nano),
		"deployment_units": []map[string]any{{
			"id": unitID.String(), "key": "api", "runtime_type": "compose", "endpoint_ref": "node-1",
			"reconcile_mode": "auto_apply", "ownership_mode": "bahia_managed", "implicit": false,
			"runtime_config": map[string]any{"image": "ghcr.io/acme/api:1"},
		}},
	})
	require.NoError(t, err)
	ev := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: nostr.Timestamp(time.Now().Unix()), Tags: tags, Content: string(content)}
	require.NoError(t, ev.Sign(sk))
	details, err := DecodeEnvironmentDetails(ev)
	require.NoError(t, err)
	require.NotNil(t, details)
	assert.Equal(t, envID, details.ID)
	assert.Equal(t, map[string]any{"region": "west"}, details.LoomWorkerSelector)
	assert.Equal(t, map[string]any{"type": "compose"}, details.RuntimeConfig)
	assert.Equal(t, "api", details.Targeting.DefaultUnitKey)
	assert.Equal(t, created, details.CreatedAt)
	require.Len(t, details.DeploymentUnits, 1)
	assert.Equal(t, unitID, details.DeploymentUnits[0].ID)
	assert.Equal(t, envID, details.DeploymentUnits[0].EnvironmentID)
	assert.Equal(t, "node-1", details.DeploymentUnits[0].EndpointRef)
	assert.Equal(t, map[string]any{"image": "ghcr.io/acme/api:1"}, details.DeploymentUnits[0].RuntimeConfig)
	assert.False(t, details.DeploymentUnits[0].Implicit)

	implicitContent, err := json.Marshal(map[string]any{
		"id": envID.String(), "name": "empty", "runtime_config": map[string]any{"type": "compose"},
		"targeting":        map[string]any{"default_unit_key": "main"},
		"deployment_units": []map[string]any{{"key": "main", "implicit": true}},
	})
	require.NoError(t, err)
	ev.Content = string(implicitContent)
	require.NoError(t, ev.Sign(sk))
	implicit, err := DecodeEnvironmentDetails(ev)
	require.NoError(t, err)
	require.Len(t, implicit.DeploymentUnits, 1)
	assert.Equal(t, "main", implicit.DeploymentUnits[0].Key)
	assert.Equal(t, envID, implicit.DeploymentUnits[0].EnvironmentID)
	assert.True(t, implicit.DeploymentUnits[0].Implicit)
}

func TestDecodeTombstoneReturnsNil(t *testing.T) {
	sk := nostr.Generate()
	svcID := uuid.New()

	_, envTags := nostrpool.ControlStateEnvelope(kinds.ServiceRegistry, svcID.String(), true)
	content := map[string]any{"deleted": true, "id": svcID.String()}
	contentJSON, _ := json.Marshal(content)
	ev := nostr.Event{
		Kind:      nostr.Kind(kinds.CASControlState),
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Tags:      envTags,
		Content:   string(contentJSON),
	}
	require.NoError(t, ev.Sign(sk))

	svc, err := DecodeService(ev)
	require.NoError(t, err)
	assert.Nil(t, svc, "tombstone should decode to nil")
}

func TestDecodeControlStateEvent(t *testing.T) {
	sk := nostr.Generate()
	svcID := uuid.New()
	ev := makeServiceEvent(t, sk, svcID, "some-service", nostr.Timestamp(time.Now().Unix()))

	decoded, err := DecodeControlStateEvent(ev)
	require.NoError(t, err)
	assert.Equal(t, "service", decoded.Domain)
	assert.Equal(t, "registry", decoded.Entity)
	assert.Equal(t, kinds.ServiceRegistry, decoded.LegacyKind)
	assert.False(t, decoded.Deleted)
}

func TestDecodeWrongKindErrors(t *testing.T) {
	sk := nostr.Generate()
	ev := nostr.Event{
		Kind:      1, // not 30900
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Content:   "{}",
	}
	require.NoError(t, ev.Sign(sk))

	_, err := DecodeControlStateEvent(ev)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not cp-state kind")
}

func TestDecodeGenericFamilies(t *testing.T) {
	// Test that all families produce valid decoded events.
	sk := nostr.Generate()
	families := nostrpool.CPStateFamilyTopics()
	for _, fam := range families {
		t.Run(fam.Topic, func(t *testing.T) {
			_, envTags := nostrpool.ControlStateEnvelope(fam.LegacyKind, "test-id", false)
			ev := nostr.Event{
				Kind:      nostr.Kind(kinds.CASControlState),
				CreatedAt: nostr.Timestamp(time.Now().Unix()),
				Tags:      envTags,
				Content:   `{"deleted":false}`,
			}
			require.NoError(t, ev.Sign(sk))

			decoded, err := DecodeControlStateEvent(ev)
			require.NoError(t, err)
			assert.Equal(t, fam.Domain, decoded.Domain)
			assert.Equal(t, fam.Entity, decoded.Entity)
		})
	}
}

// ---------------------------------------------------------------------------
// Domain topics and filter building
// ---------------------------------------------------------------------------

func TestBuildDomainTopicsCoversAllFamilies(t *testing.T) {
	topics := buildDomainTopics()
	// All families from the projector's table must appear.
	families := nostrpool.CPStateFamilyTopics()
	for _, fam := range families {
		domain := fam.Domain
		if fam.Domain == "service" && fam.Entity == "state" {
			domain = "state"
		}
		topicList, ok := topics[domain]
		if !assert.True(t, ok, "domain %q missing from topics map", fam.Domain) {
			continue
		}
		assert.Contains(t, topicList, fam.Topic, "topic %q missing from domain %q", fam.Topic, fam.Domain)
	}
}

func TestHashFilterIsDeterministic(t *testing.T) {
	f := nostr.Filter{
		Kinds:   []nostr.Kind{nostr.Kind(kinds.CASControlState)},
		Authors: []nostr.PubKey{nostr.MustPubKeyFromHex("0000000000000000000000000000000000000000000000000000000000000001")},
		Tags:    nostr.TagMap{"t": {"service-registry", "service-state"}},
	}
	h1 := hashFilter(f)
	h2 := hashFilter(f)
	assert.Equal(t, h1, h2)
	assert.Len(t, h1, 16)
}

// ---------------------------------------------------------------------------
// NewNostrClient validation
// ---------------------------------------------------------------------------

func TestNewNostrClientValidation(t *testing.T) {
	pool := &fakePool{}

	_, err := NewNostrClient(NostrClientConfig{})
	assert.ErrorContains(t, err, "StorePath")

	_, err = NewNostrClient(NostrClientConfig{StorePath: "/tmp/test"})
	assert.ErrorContains(t, err, "ServicePubkey")

	_, err = NewNostrClient(NostrClientConfig{StorePath: "/tmp/test", ServicePubkey: "abc"})
	assert.ErrorContains(t, err, "Pool")

	_, err = NewNostrClient(NostrClientConfig{StorePath: "/tmp/test", ServicePubkey: "abc", Pool: pool})
	assert.ErrorContains(t, err, "invalid service pubkey")
}
