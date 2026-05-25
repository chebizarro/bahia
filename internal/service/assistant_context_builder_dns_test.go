package service

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestAssistantContextBuilderIncludesDNSContextWhenRegistryProvided(t *testing.T) {
	ctx := context.Background()
	zoneID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	registry := assistantContextDNSRegistry{
		zones: []domain.DNSZone{{Name: "prod.internal", Visibility: domain.ZoneVisibilityInternal, BackendRef: "coredns-primary"}},
		endpoints: []domain.DNSEndpoint{{
			FQDN:        "api.prod.internal",
			Family:      domain.DNSEndpointFamilyService,
			Zone:        "prod.internal",
			Address:     "10.0.0.12",
			Health:      domain.HealthStatusHealthy,
			DriftStatus: domain.DriftStatusInSync,
		}},
		policies: []domain.DNSPolicy{{ID: uuid.MustParse("22222222-2222-4222-8222-222222222222"), Name: "internal-only", ZoneID: &zoneID, Enabled: true}},
	}
	builder := NewAssistantContextBuilder(nil, nil, nil, registry, nil, AssistantContextBuilderConfig{})

	got, err := builder.BuildContext(ctx, nil, nil, "")
	if err != nil {
		t.Fatalf("BuildContext returned error: %v", err)
	}
	assertContains(t, got, "## DNS Zones")
	assertContains(t, got, "- name=prod.internal visibility=internal backend=coredns-primary")
	assertContains(t, got, "## DNS Endpoints")
	assertContains(t, got, "- fqdn=api.prod.internal family=service zone=prod.internal address=10.0.0.12 type=A health=healthy drift=in_sync")
	assertContains(t, got, "## DNS Policies")
	assertContains(t, got, "- id=22222222-2222-4222-8222-222222222222 name=internal-only zone=11111111-1111-4111-8111-111111111111 enabled=true")
}

func TestAssistantContextBuilderOmitsDNSContextWhenRegistryMissing(t *testing.T) {
	builder := NewAssistantContextBuilder(nil, nil, nil, nil, nil, AssistantContextBuilderConfig{})

	got, err := builder.BuildContext(context.Background(), nil, nil, "")
	if err != nil {
		t.Fatalf("BuildContext returned error: %v", err)
	}
	for _, section := range []string{"## DNS Zones", "## DNS Endpoints", "## DNS Policies"} {
		if strings.Contains(got, section) {
			t.Fatalf("context contains unexpected DNS section %q:\n%s", section, got)
		}
	}
}

type assistantContextDNSRegistry struct {
	zones     []domain.DNSZone
	endpoints []domain.DNSEndpoint
	policies  []domain.DNSPolicy
}

func (r assistantContextDNSRegistry) ListDNSEndpoints(context.Context) ([]domain.DNSEndpoint, error) {
	return append([]domain.DNSEndpoint(nil), r.endpoints...), nil
}

func (r assistantContextDNSRegistry) ListDNSZones(context.Context) ([]domain.DNSZone, error) {
	return append([]domain.DNSZone(nil), r.zones...), nil
}

func (r assistantContextDNSRegistry) ListDNSPolicies(context.Context) ([]domain.DNSPolicy, error) {
	return append([]domain.DNSPolicy(nil), r.policies...), nil
}

func assertContains(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("context missing %q:\n%s", want, got)
	}
}
