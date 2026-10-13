package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/google/uuid"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type hostnameTestPublisher struct {
	mu   sync.Mutex
	key  nostr.SecretKey
	last map[string]nostr.Timestamp
}

func (p *hostnameTestPublisher) PublishHostnameRecord(_ context.Context, family kinds.CPStateFamily, coordinate string, deleted bool, content string) (*nostr.Event, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	at := nostr.Now()
	if at <= p.last[coordinate] {
		at = p.last[coordinate] + 1
	}
	p.last[coordinate] = at
	kind, tags := nostradapter.ControlStateEnvelope(int(family), coordinate, deleted)
	ev := &nostr.Event{Kind: nostr.Kind(kind), CreatedAt: at, Tags: tags, Content: content}
	if err := ev.Sign(p.key); err != nil {
		return nil, err
	}
	return ev, nil
}

type hostnameTestRoutes struct {
	mu        sync.Mutex
	phases    []string
	failPhase string
}

func (*hostnameTestRoutes) CheckClaim(context.Context, *domain.DesiredPublicRoutePlan, uuid.UUID, bool) error {
	return nil
}
func (r *hostnameTestRoutes) Withdraw(ctx context.Context, _ *domain.DesiredPublicRoutePlan, phase func(context.Context, string) error) error {
	for _, step := range []string{"dns_withdrawn", "tunnel_withdrawn"} {
		r.mu.Lock()
		r.phases = append(r.phases, step)
		fail := r.failPhase == step
		if fail {
			r.failPhase = ""
		}
		r.mu.Unlock()
		if err := phase(ctx, step); err != nil {
			return err
		}
		if fail {
			return errors.New("interrupted after " + step)
		}
	}
	return nil
}

type hostnameHarness struct {
	store                                 *localstore.Store
	handler                               *HostnameIntentHandler
	processor                             *IntentProcessor
	actor                                 nostr.SecretKey
	service                               nostr.SecretKey
	org, serviceID, environmentID, unitID uuid.UUID
	plan                                  *domain.DesiredPublicRoutePlan
	routes                                *hostnameTestRoutes
	statuses                              *[]nostr.Event
	statusMu                              *sync.Mutex
	publisher                             *hostnameTestPublisher
}

func newHostnameHarness(t *testing.T, path string, prior *hostnameHarness) *hostnameHarness {
	t.Helper()
	store, err := localstore.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	h := &hostnameHarness{store: store, routes: &hostnameTestRoutes{}, statuses: new([]nostr.Event), statusMu: new(sync.Mutex)}
	if prior != nil {
		h.actor, h.service, h.org, h.serviceID, h.environmentID, h.unitID, h.plan, h.routes, h.statuses, h.statusMu = prior.actor, prior.service, prior.org, prior.serviceID, prior.environmentID, prior.unitID, prior.plan, prior.routes, prior.statuses, prior.statusMu
	} else {
		h.actor, h.service = nostr.Generate(), nostr.Generate()
		h.org, h.serviceID, h.environmentID, h.unitID = uuid.New(), uuid.New(), uuid.New(), uuid.New()
		h.plan = hostnameTestPlan(h.serviceID, h.environmentID, h.unitID)
		seedHostnameOwner(t, store, h.service, h.org, h.serviceID, kinds.ServiceRegistry)
		seedHostnameOwner(t, store, h.service, h.org, h.environmentID, kinds.EnvironmentRegistry)
	}
	publisher := &hostnameTestPublisher{key: h.service, last: map[string]nostr.Timestamp{}}
	if prior != nil {
		publisher.last = prior.publisher.last
	}
	h.publisher = publisher
	h.handler, err = NewHostnameIntentHandler(store, h.service.Public().Hex(), publisher, h.routes, NewTrustSet(nil, zap.NewNop(), WithBootstrapOwners(map[string]string{h.org.String(): h.actor.Public().Hex()})), func() bool { return true })
	require.NoError(t, err)
	status := NewIntentStatusPublisher(func(_ context.Context, ev nostr.Event) error {
		h.statusMu.Lock()
		defer h.statusMu.Unlock()
		*h.statuses = append(*h.statuses, ev)
		return nil
	}, keyer.NewPlainKeySigner(h.service), zap.NewNop())
	h.processor = NewIntentProcessor(NewTrustSet(nil, zap.NewNop(), WithBootstrapOwners(map[string]string{h.org.String(): h.actor.Public().Hex()})), store, status, IntentProcessorConfig{EnabledDomains: map[string]bool{"hostname": true}}, zap.NewNop())
	h.processor.RegisterHandler("hostname", h.handler)
	return h
}
func seedHostnameOwner(t *testing.T, store *localstore.Store, key nostr.SecretKey, org, id uuid.UUID, kind int) {
	t.Helper()
	wire, tags := nostradapter.ControlStateEnvelope(kind, id.String(), false)
	ev := nostr.Event{Kind: nostr.Kind(wire), CreatedAt: nostr.Now(), Tags: tags, Content: fmt.Sprintf(`{"org_id":%q}`, org.String())}
	require.NoError(t, ev.Sign(key))
	_, err := store.SaveEvent(ev)
	require.NoError(t, err)
}
func hostnameTestPlan(svc, env, unit uuid.UUID) *domain.DesiredPublicRoutePlan {
	host := "api.example.com"
	return &domain.DesiredPublicRoutePlan{SchemaVersion: "1", ServiceID: svc, EnvironmentID: env, DeploymentUnitID: unit, Hostname: host, Zone: "example.com", BackendRef: "edge", Provider: "cloudflare_tunnel", ProviderConfigHash: "sha256:config", DNS: domain.DesiredPublicRouteDNS{Type: "CNAME", Name: host, Value: "tunnel.example.net", TTL: 1, Proxied: true, SourceCoordinate: domain.PublicRouteCoordinate(svc, env, unit)}, Tunnel: domain.DesiredPublicRouteTunnel{TunnelRef: "tunnel", Hostname: host, OriginURL: "http://edge.internal:8080"}, Proxy: domain.DesiredPublicRouteProxy{HostMatch: host, UpstreamScheme: "http", UpstreamHost: "edge.internal", UpstreamPort: 8080, HealthPath: "/health"}, TLS: domain.DesiredPublicRouteTLS{Mode: "managed", Provider: "cloudflare"}, Operations: []domain.DesiredPublicRouteChange{{Order: 1, Resource: "dns", Action: "upsert"}}, Rollback: []domain.DesiredPublicRouteChange{{Order: 1, Resource: "dns", Action: "withdraw"}}}
}
func (h *hostnameHarness) intent(t *testing.T, op string, expected *time.Time, owner uuid.UUID) *nostr.Event {
	t.Helper()
	id := uuid.Must(uuid.NewV7()).String()
	payload := hostnameIntentPayload{Hostname: h.plan.Hostname, ServiceID: owner, EnvironmentID: h.environmentID, DeploymentUnitID: h.unitID, IntentID: id}
	if op == "set" {
		cp := *h.plan
		cp.ServiceID = owner
		cp.DNS.SourceCoordinate = domain.PublicRouteCoordinate(owner, h.environmentID, h.unitID)
		payload.Plan = &cp
	}
	if expected != nil {
		payload.ExpectedUpdatedAt = expected.UTC().Format(time.RFC3339Nano)
	}
	content, err := json.Marshal(payload)
	require.NoError(t, err)
	ev := &nostr.Event{Kind: 30900, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", kinds.HostnameReservationDTag(h.plan.Hostname)}, {"domain", "hostname"}, {"schema", HostnameIntentSchema}, {"op", op}, {"org", h.org.String()}, {"intent_id", id}, {"t", "bahia-intent"}}, Content: string(content)}
	require.NoError(t, ev.Sign(h.actor))
	return ev
}
func TestHostnameConcurrentClaimConflictAndDBLessOwner(t *testing.T) {
	h := newHostnameHarness(t, filepath.Join(t.TempDir(), "events.db"), nil)
	first := h.intent(t, "set", nil, h.serviceID)
	secondOwner := uuid.New()
	seedHostnameOwner(t, h.store, h.service, h.org, secondOwner, kinds.ServiceRegistry)
	second := h.intent(t, "set", nil, secondOwner)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, ev := range []*nostr.Event{first, second} {
		wg.Add(1)
		go func(i int, ev *nostr.Event) {
			defer wg.Done()
			errs[i] = h.processor.ProcessRelayIntent(context.Background(), ev)
		}(i, ev)
	}
	wg.Wait()
	success, conflict := 0, 0
	for _, err := range errs {
		if err == nil {
			success++
		} else if IsRevisionConflict(err) {
			conflict++
		} else {
			t.Fatalf("unexpected: %v", err)
		}
	}
	require.Equal(t, 1, success)
	require.Equal(t, 1, conflict)
	current, err := h.handler.Current(t.Context(), h.plan.Hostname)
	require.NoError(t, err)
	require.NotNil(t, current)
	require.False(t, current.Deleted)
	require.Len(t, *h.statuses, 2)
	seen := map[string]bool{}
	for _, ev := range *h.statuses {
		seen[extractTag(ev, "status")] = true
	}
	require.True(t, seen["accepted"])
	require.True(t, seen["conflict"])
}
func TestHostnameReleaseReclaimAndRestartRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	h := newHostnameHarness(t, path, nil)
	require.NoError(t, h.processor.ProcessRelayIntent(t.Context(), h.intent(t, "set", nil, h.serviceID)))
	current, err := h.handler.Current(t.Context(), h.plan.Hostname)
	require.NoError(t, err)
	release := h.intent(t, "remove", &current.UpdatedAt, h.serviceID)
	h.routes.failPhase = "dns_withdrawn"
	require.ErrorContains(t, h.processor.ProcessRelayIntent(t.Context(), release), "interrupted after dns_withdrawn")
	require.False(t, h.processor.IsProcessed(extractTag(*release, "intent_id")))
	receipt, err := h.handler.latest(kinds.HostnameRecoveryDTag(h.plan.Hostname), kinds.CPStateFamilyHostnameRecovery, kinds.CPStateTopicHostnameRecovery)
	require.NoError(t, err)
	require.NotNil(t, receipt)
	require.Contains(t, receipt.Content, "dns_withdrawn")
	parsed, err := ParseIntent(release)
	require.NoError(t, err)
	parsed.Actor = release.PubKey.Hex()
	require.NoError(t, h.handler.publishRecovery(t.Context(), parsed, current, "prepared"))
	retained, err := h.handler.latest(kinds.HostnameRecoveryDTag(h.plan.Hostname), kinds.CPStateFamilyHostnameRecovery, kinds.CPStateTopicHostnameRecovery)
	require.NoError(t, err)
	require.Contains(t, retained.Content, "dns_withdrawn", "restart must not regress canonical receipt")
	resumed := newHostnameHarness(t, path, h)
	require.NoError(t, resumed.processor.ProcessRelayIntent(t.Context(), release))
	tombstone, err := resumed.handler.Current(t.Context(), h.plan.Hostname)
	require.NoError(t, err)
	require.True(t, tombstone.Deleted)
	require.NoError(t, resumed.processor.ProcessRelayIntent(t.Context(), resumed.intent(t, "set", &tombstone.UpdatedAt, h.serviceID)))
	reclaimed, err := resumed.handler.Current(t.Context(), h.plan.Hostname)
	require.NoError(t, err)
	require.False(t, reclaimed.Deleted)
	require.Equal(t, []string{"dns_withdrawn", "dns_withdrawn", "tunnel_withdrawn"}, resumed.routes.phases)
}

func TestHostnameObservedIntentWinnerLatestThenLowestID(t *testing.T) {
	for _, tie := range []bool{false, true} {
		name := "newer"
		if tie {
			name = "lowest_id_tie"
		}
		t.Run(name, func(t *testing.T) {
			h := newHostnameHarness(t, filepath.Join(t.TempDir(), "events.db"), nil)
			other := nostr.Generate()
			members := map[string]domain.Role{h.actor.Public().Hex(): domain.RoleOwner, other.Public().Hex(): domain.RoleOwner}
			h.handler.trust.SetRelayMembers(h.org.String(), members)
			h.processor.trustSet.SetRelayMembers(h.org.String(), members)
			a := h.intent(t, "set", nil, h.serviceID)
			b := h.intent(t, "set", nil, h.serviceID)
			if !tie {
				b.CreatedAt = a.CreatedAt + 1
			} else {
				b.CreatedAt = a.CreatedAt
			}
			require.NoError(t, b.Sign(other))
			for _, ev := range []*nostr.Event{a, b} {
				_, err := h.store.SaveEvent(*ev)
				require.NoError(t, err)
			}
			loser, winner := a, b
			if tie && string(a.ID[:]) < string(b.ID[:]) {
				loser, winner = b, a
			}
			err := h.processor.ProcessRelayIntent(t.Context(), loser)
			var superseded *intentSupersededError
			require.ErrorAs(t, err, &superseded)
			require.Equal(t, winner.ID.Hex(), superseded.winner)
			require.NoError(t, h.processor.ProcessRelayIntent(t.Context(), winner))
			current, err := h.handler.Current(t.Context(), h.plan.Hostname)
			require.NoError(t, err)
			require.Equal(t, winner.ID.Hex(), current.IntentEventID)
			statuses := map[string]bool{}
			for _, event := range *h.statuses {
				statuses[extractTag(event, "status")] = true
			}
			require.True(t, statuses["superseded"])
			require.True(t, statuses["accepted"])
		})
	}
}

func TestHostnamePreserveRequiresCurrentRevisionAndDoesNotRepublish(t *testing.T) {
	h := newHostnameHarness(t, filepath.Join(t.TempDir(), "events.db"), nil)
	require.NoError(t, h.processor.ProcessRelayIntent(t.Context(), h.intent(t, "set", nil, h.serviceID)))
	current, err := h.handler.Current(t.Context(), h.plan.Hostname)
	require.NoError(t, err)
	stale := h.intent(t, "preserve", nil, h.serviceID)
	require.Error(t, h.processor.ProcessRelayIntent(t.Context(), stale))
	before, err := h.handler.latest(kinds.HostnameReservationDTag(h.plan.Hostname), kinds.CPStateFamilyHostnameReservation, kinds.CPStateTopicHostnameReservation)
	require.NoError(t, err)
	require.NoError(t, h.processor.ProcessRelayIntent(t.Context(), h.intent(t, "preserve", &current.UpdatedAt, h.serviceID)))
	after, err := h.handler.latest(kinds.HostnameReservationDTag(h.plan.Hostname), kinds.CPStateFamilyHostnameReservation, kinds.CPStateTopicHostnameReservation)
	require.NoError(t, err)
	require.Equal(t, before.ID, after.ID)
}

func TestHostnameRequireReleasedFollowsCanonicalTombstone(t *testing.T) {
	h := newHostnameHarness(t, filepath.Join(t.TempDir(), "events.db"), nil)
	ctx := t.Context()
	require.NoError(t, h.processor.ProcessRelayIntent(ctx, h.intent(t, "set", nil, h.serviceID)))
	require.Error(t, h.handler.RequireReleased(ctx, h.plan))
	current, err := h.handler.Current(ctx, h.plan.Hostname)
	require.NoError(t, err)
	require.NoError(t, h.processor.ProcessRelayIntent(ctx, h.intent(t, "remove", &current.UpdatedAt, h.serviceID)))
	require.NoError(t, h.handler.RequireReleased(ctx, h.plan))
	wrong := *h.plan
	wrong.ServiceID = uuid.New()
	wrong.DNS.SourceCoordinate = domain.PublicRouteCoordinate(wrong.ServiceID, wrong.EnvironmentID, wrong.DeploymentUnitID)
	require.Error(t, h.handler.RequireReleased(ctx, &wrong))
}
