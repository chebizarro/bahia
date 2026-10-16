package identityrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	adapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

func hostnameTestPlan() domain.DesiredPublicRoutePlan {
	svc, env, unit := uuid.New(), uuid.New(), uuid.New()
	host := "api.example.com"
	return domain.DesiredPublicRoutePlan{
		SchemaVersion: domain.PublicRouteSchemaVersion, ServiceID: svc, EnvironmentID: env, DeploymentUnitID: unit,
		Hostname: host, Zone: "example.com", BackendRef: "edge", Provider: "cloudflare_tunnel", ProviderConfigHash: "sha256:config",
		DNS:        domain.DesiredPublicRouteDNS{Type: "CNAME", Name: host, Value: "tunnel.example.net", TTL: 1, Proxied: true, SourceCoordinate: domain.PublicRouteCoordinate(svc, env, unit)},
		Tunnel:     domain.DesiredPublicRouteTunnel{TunnelRef: "tunnel-1", Hostname: host, OriginURL: "http://edge.internal:8080"},
		Proxy:      domain.DesiredPublicRouteProxy{HostMatch: host, UpstreamScheme: "http", UpstreamHost: "edge.internal", UpstreamPort: 8080, HealthPath: "/healthz"},
		TLS:        domain.DesiredPublicRouteTLS{Mode: "managed", Provider: "cloudflare"},
		Operations: []domain.DesiredPublicRouteChange{{Order: 1, Resource: "application", Action: "apply"}, {Order: 2, Resource: "edge", Action: "upsert"}},
		Rollback:   []domain.DesiredPublicRouteChange{{Order: 1, Resource: "edge", Action: "restore"}},
	}
}

func hostnameFixture(t *testing.T) (*HostnameRecoveryAdapter, *registryTestRelay, *registryTestLocal, nostr.Keyer, []nostr.Event) {
	t.Helper()
	oldKey := signer(t)
	plan := hostnameTestPlan()
	now := time.Unix(1_700_000_000, 0).UTC()
	reservation := domain.HostnameReservation{Hostname: plan.Hostname, OrgID: uuid.New(), ServiceID: plan.ServiceID, EnvironmentID: plan.EnvironmentID, DeploymentUnitID: plan.DeploymentUnitID, Plan: &plan, UpdatedAt: now, IntentID: uuid.NewString(), IntentEventID: "request-id", RequestedBy: "operator"}
	if err := reservation.Validate(); err != nil {
		t.Fatal(err)
	}
	receipt := controlplane.HostnameRecoveryRecord{Hostname: plan.Hostname, IntentID: reservation.IntentID, IntentEventID: reservation.IntentEventID, RequestedBy: reservation.RequestedBy, Phase: "prepared", UpdatedAt: now, Plan: plan}
	var heads []nostr.Event
	for _, item := range []struct {
		family kinds.CPStateFamily
		d      string
		record any
	}{
		{kinds.CPStateFamilyHostnameReservation, kinds.HostnameReservationDTag(plan.Hostname), reservation},
		{kinds.CPStateFamilyHostnameRecovery, kinds.HostnameRecoveryDTag(plan.Hostname), receipt},
	} {
		body, err := json.Marshal(item.record)
		if err != nil {
			t.Fatal(err)
		}
		kind, tags := adapter.ControlStateEnvelope(int(item.family), item.d, false)
		heads = append(heads, signedRegistryHead(t, oldKey, kind, tags, string(body), int64(100+len(heads))))
	}
	base, relay, local, newKey := registryFixture(t, adapter.KindServiceRegistry, heads)
	return &HostnameRecoveryAdapter{OldPubkey: heads[0].PubKey, Source: relay, Local: local, Ledger: base.Ledger}, relay, local, newKey, heads
}

func TestHostnameRecoveryRoundTripIdempotenceAndEquivalence(t *testing.T) {
	a, relay, local, key, heads := hostnameFixture(t)
	ctx := context.Background()
	ids, err := a.Inventory(ctx)
	if err != nil || len(ids) != 2 || !slices.Contains(ids, heads[0].ID.Hex()) {
		t.Fatalf("inventory %v: %v", ids, err)
	}
	if _, err := a.Reassert(ctx, key); err != nil {
		t.Fatal(err)
	}
	if relay.publishes != 2 || local.writes != 2 {
		t.Fatalf("writes relay=%d local=%d", relay.publishes, local.writes)
	}
	if proof, err := a.Verify(ctx); err != nil || len(proof) != 2 {
		t.Fatalf("verify %v: %v", proof, err)
	}
	if _, err := a.Reassert(ctx, key); err != nil {
		t.Fatal(err)
	}
	if relay.publishes != 2 || local.writes != 2 {
		t.Fatal("idempotent rerun wrote heads")
	}
	bad := signedRegistryHead(t, key, int(heads[0].Kind), heads[0].Tags, `{"changed":true}`, time.Now().Unix()+100)
	relay.events = append(relay.events, bad)
	local.events = append(local.events, bad)
	if _, err := a.Verify(ctx); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("divergent verify: %v", err)
	}
	if _, err := a.Reassert(ctx, key); err == nil || relay.publishes != 2 {
		t.Fatalf("divergent reassert: %v", err)
	}
}

func TestHostnameRecoveryRequiresEOSE(t *testing.T) {
	a, relay, _, key, _ := hostnameFixture(t)
	relay.readErr = errors.New("EOSE incomplete")
	if _, err := a.Inventory(context.Background()); err == nil {
		t.Fatal("EOSE failure accepted as absence")
	}
	if _, err := a.Reassert(context.Background(), key); err == nil || relay.publishes != 0 {
		t.Fatalf("EOSE failure published: %v", err)
	}
}
