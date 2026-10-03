package controlplane

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type d72DNSCanonical struct{ live, tombstones int }

func (p *d72DNSCanonical) PublishZone(context.Context, domain.DNSZone) error { p.live++; return nil }
func (p *d72DNSCanonical) PublishZoneTombstone(context.Context, string) error {
	p.tombstones++
	return nil
}
func (p *d72DNSCanonical) PublishPolicy(context.Context, domain.DNSPolicy) error {
	p.live++
	return nil
}
func (p *d72DNSCanonical) PublishPolicyTombstone(context.Context, uuid.UUID) error {
	p.tombstones++
	return nil
}
func (p *d72DNSCanonical) PublishEndpoint(context.Context, domain.DNSEndpoint) error {
	p.live++
	return nil
}
func (p *d72DNSCanonical) PublishEndpointTombstone(context.Context, domain.DNSEndpoint) error {
	p.tombstones++
	return nil
}
func (p *d72DNSCanonical) PublishBackend(context.Context, domain.DNSBackendState) error {
	p.live++
	return nil
}
func (p *d72DNSCanonical) PublishBackendTombstone(context.Context, string) error {
	p.tombstones++
	return nil
}

func (o *recordingDNSOperator) RetireZone(context.Context, domain.DNSZone) error { return nil }

func d72DNSFixture(t *testing.T) (*service.DNSMutationService, *recordingDNSOperator, *d72DNSCanonical) {
	t.Helper()
	store, err := localstore.OpenOutbox(t.TempDir() + "/outbox.db")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	op := &recordingDNSOperator{backends: map[string]bool{"primary": true}}
	canonical := &d72DNSCanonical{}
	svc := &service.DNSMutationService{
		Zones:     repository.NewLocalDNSZoneRepository(store),
		Policies:  repository.NewLocalDNSPolicyRepository(store),
		Endpoints: repository.NewLocalDNSEndpointRepository(store),
		Backends:  repository.NewLocalDNSBackendRepository(store),
		Canonical: canonical, Reconciler: op,
	}
	require.NoError(t, svc.Backends.Upsert(context.Background(), &domain.DNSBackendState{Ref: "primary", Type: domain.DNSBackendTypeCoreDNS}))
	return svc, op, canonical
}

func d72Endpoint() domain.DNSEndpoint {
	return domain.DNSEndpoint{Family: domain.DNSEndpointFamilyService, Name: "api", Environment: "prod", Zone: "example.test", FQDN: "api.example.test", Address: "192.0.2.10", Source: "operator", Coordinate: "endpoint:service:api:prod"}
}

func TestD72DNSIntentsDurableCRUD(t *testing.T) {
	ctx := context.Background()
	for _, op := range []string{"zone-update", "zone-delete", "endpoint-create", "endpoint-update", "endpoint-delete", "backend-create", "backend-update", "backend-delete", "policy-update", "policy-delete"} {
		t.Run(op, func(t *testing.T) {
			svc, operator, canonical := d72DNSFixture(t)
			zone := domain.DNSZone{Name: "example.test", Visibility: domain.ZoneVisibilityInternal, BackendRef: "primary", TTL: 60}
			if op != "zone-update" && op != "zone-delete" {
				require.NoError(t, svc.Zones.Create(ctx, &zone))
			}
			var coordinate string
			var content map[string]interface{}
			var updatedAt time.Time
			switch op {
			case "zone-update", "zone-delete":
				require.NoError(t, svc.Zones.Create(ctx, &zone))
				coordinate = "zone:example.test"
				updatedAt = zone.UpdatedAt
				if op == "zone-update" {
					content = map[string]interface{}{"name": zone.Name, "visibility": "internal", "backend_ref": "primary", "ttl": 120}
				} else {
					content = map[string]interface{}{"name": zone.Name}
				}
			case "endpoint-create", "endpoint-update", "endpoint-delete":
				endpoint := d72Endpoint()
				coordinate = endpoint.Coordinate
				if op != "endpoint-create" {
					require.NoError(t, svc.Endpoints.Upsert(ctx, &endpoint))
					updatedAt = endpoint.UpdatedAt
				}
				if op == "endpoint-delete" {
					content = map[string]interface{}{"coordinate": coordinate}
				} else {
					content = map[string]interface{}{"family": "service", "name": "api", "environment": "prod", "zone": "example.test", "fqdn": "api.example.test", "coordinate": coordinate, "address": "192.0.2.20", "source": "operator"}
				}
			case "backend-create", "backend-update", "backend-delete":
				backend := domain.DNSBackendState{Ref: "secondary", Type: domain.DNSBackendTypeCoreDNS, Health: domain.HealthStatusHealthy}
				coordinate = "dnsbackend:secondary"
				if op != "backend-create" {
					require.NoError(t, svc.Backends.Upsert(ctx, &backend))
					updatedAt = backend.UpdatedAt
				}
				if op == "backend-delete" {
					content = map[string]interface{}{"ref": backend.Ref}
				} else {
					content = map[string]interface{}{"ref": backend.Ref, "type": "coredns", "health": "healthy"}
				}
			case "policy-update", "policy-delete":
				policy := domain.DNSPolicy{ID: uuid.New(), Name: "ttl", Enabled: true, Rules: []domain.DNSPolicyRule{{Action: domain.DNSPolicyAction{TTLOverride: intPtrD72(60)}}}}
				require.NoError(t, svc.Policies.Create(ctx, &policy))
				coordinate = "dnspolicy:" + policy.ID.String()
				updatedAt = policy.UpdatedAt
				if op == "policy-delete" {
					content = map[string]interface{}{"id": policy.ID.String()}
				} else {
					content = map[string]interface{}{"id": policy.ID.String(), "name": "ttl", "enabled": true, "rules": []interface{}{map[string]interface{}{"match": map[string]interface{}{}, "action": map[string]interface{}{"ttl_override": 120}}}}
				}
			}
			if !updatedAt.IsZero() {
				content["expected_updated_at"] = updatedAt.Format(time.RFC3339Nano)
			}
			staleContent := make(map[string]interface{}, len(content))
			for key, value := range content {
				staleContent[key] = value
			}
			staleContent["expected_updated_at"] = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
			staleProcessor, staleStatuses := d70Processor(t, "dns", testPubkey, NewDNSIntentHandler(operator, canonical, svc))
			require.Error(t, staleProcessor.ProcessInProcess(ctx, d70Intent("dns", op, coordinate, testPubkey, staleContent)))
			require.Equal(t, "conflict", tagValueNostr(staleStatuses.events[0].Tags, "status"))
			require.Zero(t, canonical.live+canonical.tombstones)
			processor, statuses := d70Processor(t, "dns", testPubkey, NewDNSIntentHandler(operator, canonical, svc))
			intent := d70Intent("dns", op, coordinate, testPubkey, content)
			assertD70AcceptedReplayAndUnauthorized(t, processor, statuses, intent, func() int { return canonical.live + canonical.tombstones })
			if op == "zone-delete" || op == "endpoint-delete" || op == "backend-delete" || op == "policy-delete" {
				require.Equal(t, 1, canonical.tombstones)
			} else {
				require.Equal(t, 1, canonical.live)
			}
			switch op {
			case "zone-update", "zone-delete":
				stored, err := svc.Zones.Get(ctx, "example.test")
				require.NoError(t, err)
				if op == "zone-delete" {
					require.Nil(t, stored)
				} else {
					require.Equal(t, 120, stored.TTL)
				}
			case "endpoint-create", "endpoint-update", "endpoint-delete":
				stored, err := svc.Endpoints.Get(ctx, coordinate)
				require.NoError(t, err)
				if op == "endpoint-delete" {
					require.Nil(t, stored)
				} else {
					require.Equal(t, "192.0.2.20", stored.Address)
				}
			case "backend-create", "backend-update", "backend-delete":
				stored, err := svc.Backends.Get(ctx, "secondary")
				require.NoError(t, err)
				if op == "backend-delete" {
					require.Nil(t, stored)
				} else {
					require.Equal(t, domain.DNSBackendTypeCoreDNS, stored.Type)
				}
			case "policy-update", "policy-delete":
				id, err := uuid.Parse(strings.TrimPrefix(coordinate, "dnspolicy:"))
				require.NoError(t, err)
				stored, err := svc.Policies.Get(ctx, id)
				require.NoError(t, err)
				if op == "policy-delete" {
					require.Nil(t, stored)
				} else {
					require.Equal(t, 120, *stored.Rules[0].Action.TTLOverride)
				}
			}
		})
	}
}

func intPtrD72(n int) *int { return &n }

func TestD72DeletedBackendCannotBeReusedByZone(t *testing.T) {
	svc, _, canonical := d72DNSFixture(t)
	ctx := context.Background()
	require.NoError(t, svc.DeleteBackend(ctx, "primary"))
	require.Equal(t, 1, canonical.tombstones)
	err := svc.CreateZone(ctx, &domain.DNSZone{Name: "example.test", Visibility: domain.ZoneVisibilityInternal, BackendRef: "primary", TTL: 60})
	require.ErrorContains(t, err, "not registered")
	zone, err := svc.Zones.Get(ctx, "example.test")
	require.NoError(t, err)
	require.Nil(t, zone)
}

func TestD72MLIntentsDeleteAndIdentityChange(t *testing.T) {
	ctx := context.Background()
	for _, op := range []string{"model-delete", "version-delete", "endpoint-delete", "model-update", "version-update", "endpoint-update"} {
		t.Run(op, func(t *testing.T) {
			repo, canonical := newD70MLRepo(), &d70MLCanonical{}
			registry := service.NewMLRegistryService(repo, &events.NoopPublisher{}, zap.NewNop())
			registry.SetMLCPStatePublisher(canonical)
			id, modelID, envID := uuid.New(), uuid.New(), uuid.New()
			updatedAt := time.Now().UTC()
			var coordinate string
			var content map[string]interface{}
			switch op {
			case "model-delete", "model-update":
				repo.models[id] = &domain.MLModel{ID: id, Slug: "old", Name: "Old", UpdatedAt: updatedAt}
				if op == "model-delete" {
					coordinate = "model:old"
					content = map[string]interface{}{"id": id.String()}
				} else {
					coordinate = "model:new"
					content = map[string]interface{}{"id": id.String(), "slug": "new", "name": "New"}
				}
			case "version-delete", "version-update":
				repo.models[modelID] = &domain.MLModel{ID: modelID, Slug: "sample", Name: "Sample"}
				repo.versions[id] = &domain.MLModelVersion{ID: id, ModelID: modelID, Version: "v1", UpdatedAt: updatedAt}
				coordinate = "model-version:" + id.String()
				if op == "version-delete" {
					content = map[string]interface{}{"id": id.String()}
				} else {
					content = map[string]interface{}{"id": id.String(), "model_id": modelID.String(), "version": "v2", "source": map[string]interface{}{"uri": "s3://model/v2"}}
				}
			case "endpoint-delete", "endpoint-update":
				repo.endpoints[id] = &domain.MLInferenceEndpoint{ID: id, Name: "old", EnvironmentID: envID, UpdatedAt: updatedAt}
				coordinate = "endpoint:" + id.String()
				if op == "endpoint-delete" {
					content = map[string]interface{}{"id": id.String()}
				} else {
					content = map[string]interface{}{"id": id.String(), "name": "new", "environment_id": envID.String()}
				}
			}
			content["expected_updated_at"] = updatedAt.Format(time.RFC3339Nano)
			staleContent := make(map[string]interface{}, len(content))
			for key, value := range content {
				staleContent[key] = value
			}
			staleContent["expected_updated_at"] = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
			staleProcessor, staleStatuses := d70Processor(t, "ml", testPubkey, NewMLIntentHandler(registry))
			require.Error(t, staleProcessor.ProcessInProcess(ctx, d70Intent("ml", op, coordinate, testPubkey, staleContent)))
			require.Equal(t, "conflict", tagValueNostr(staleStatuses.events[0].Tags, "status"))
			require.Zero(t, repo.writes)
			processor, statuses := d70Processor(t, "ml", testPubkey, NewMLIntentHandler(registry))
			intent := d70Intent("ml", op, coordinate, testPubkey, content)
			counts := func() int {
				return repo.writes + canonical.count() + canonical.modelTombstones + canonical.versionTombstones + canonical.endpointTombstones
			}
			assertD70AcceptedReplayAndUnauthorized(t, processor, statuses, intent, counts)
			require.Equal(t, 1, repo.writes)
			if op == "model-delete" || op == "model-update" {
				require.Equal(t, 1, canonical.modelTombstones)
			}
			if op == "version-delete" || op == "version-update" {
				require.Equal(t, 1, canonical.versionTombstones)
			}
			if op == "endpoint-delete" || op == "endpoint-update" {
				require.Equal(t, 1, canonical.endpointTombstones)
			}
			switch op {
			case "model-delete":
				require.Nil(t, repo.models[id])
			case "model-update":
				require.Equal(t, "new", repo.models[id].Slug)
				require.Equal(t, 1, canonical.models)
			case "version-delete":
				require.Nil(t, repo.versions[id])
			case "version-update":
				require.Equal(t, "v2", repo.versions[id].Version)
				require.Equal(t, 1, canonical.versions)
			case "endpoint-delete":
				require.Nil(t, repo.endpoints[id])
			case "endpoint-update":
				require.Equal(t, "new", repo.endpoints[id].Name)
				require.Equal(t, 1, canonical.endpoints)
			}
		})
	}
}

func TestD72MLContextVMDualAndLegacyDispatch(t *testing.T) {
	for _, dual := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "dual"}[dual], func(t *testing.T) {
			actor := testNostrPubKeyHexFromPrivateKey(t, nostr.Generate().Hex())
			repo, canonical := newD70MLRepo(), &d70MLCanonical{}
			registry := service.NewMLRegistryService(repo, nil, zap.NewNop())
			registry.SetMLCPStatePublisher(canonical)
			var processor *IntentProcessor
			var statuses *statusCollector
			if dual {
				processor, statuses = d70Processor(t, "ml", actor, NewMLIntentHandler(registry))
			}
			h := mlRegistryContextVMHandlers{registry: registry, processor: processor}
			id := uuid.New()
			content, err := json.Marshal(map[string]any{"id": id.String(), "slug": "sample", "name": "Sample"})
			require.NoError(t, err)
			request := ContextVMRequest{Event: &nostr.Event{ID: testNostrID("d72-ml-contextvm"), PubKey: testNostrPubKeyFromHex(t, actor)}, RPC: ContextVMJSONRPCRequest{Params: content}}
			_, err = h.mutate(context.Background(), request, "model-create")
			require.NoError(t, err)
			require.Equal(t, 1, repo.writes)
			require.Equal(t, 1, canonical.models)
			if dual {
				require.Len(t, statuses.events, 1)
				require.Equal(t, "accepted", tagValueNostr(statuses.events[0].Tags, "status"))
				_, err = h.mutate(context.Background(), request, "model-create")
				require.NoError(t, err)
				require.Equal(t, 1, repo.writes)
				require.Equal(t, 1, canonical.models)
			}
		})
	}
}
