package mcp

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

type dnsEndpointListerFunc func(context.Context) ([]domain.DNSEndpoint, error)

func (f dnsEndpointListerFunc) ListDNSEndpoints(ctx context.Context) ([]domain.DNSEndpoint, error) {
	return f(ctx)
}

func TestDNSResourcesListHealthyEndpoints(t *testing.T) {
	port := 443
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{
		DNSEndpoints: dnsEndpointListerFunc(func(ctx context.Context) ([]domain.DNSEndpoint, error) {
			return []domain.DNSEndpoint{
				{
					Name:         "drydock-review",
					Environment:  "prod",
					FQDN:         "drydock-review.prod.example.test",
					Protocol:     "https",
					Address:      "10.0.0.12",
					Port:         &port,
					Health:       domain.HealthStatusHealthy,
					Capabilities: []string{"llm", "inference"},
					Runtime:      "kubernetes",
					Hardware:     "a100",
				},
				{
					Name:        "drydock-staging",
					Environment: "staging",
					FQDN:        "drydock-staging.example.test",
					Address:     "10.0.0.13",
					Health:      domain.HealthStatusUnhealthy,
				},
			}, nil
		}),
	})

	resources, err := server.GetResources(context.Background())
	if err != nil {
		t.Fatalf("GetResources returned error: %v", err)
	}
	if len(resources) != 1 {
		t.Fatalf("expected 1 healthy resource, got %d", len(resources))
	}

	resource := resources[0]
	if resource.URI != "bahia://dns/endpoint/drydock-review.prod.example.test" {
		t.Fatalf("unexpected URI: %s", resource.URI)
	}
	if resource.Name != "drydock-review.prod" {
		t.Fatalf("unexpected name: %s", resource.Name)
	}
	if resource.MIMEType != "application/json" {
		t.Fatalf("unexpected MIME type: %s", resource.MIMEType)
	}
	if resource.Description != "Healthy DNS endpoint with capabilities inference, llm; runtime kubernetes; hardware a100" {
		t.Fatalf("unexpected description: %s", resource.Description)
	}

	expectedMetadata := map[string]any{
		"protocol":     "https",
		"address":      "10.0.0.12",
		"port":         443,
		"health":       "healthy",
		"capabilities": []string{"llm", "inference"},
		"runtime":      "kubernetes",
		"hardware":     "a100",
	}
	if !reflect.DeepEqual(resource.Metadata, expectedMetadata) {
		t.Fatalf("unexpected metadata: %#v", resource.Metadata)
	}
}

func TestDNSResourcesNoListerReturnsEmpty(t *testing.T) {
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})

	resources, err := server.GetResources(context.Background())
	if err != nil {
		t.Fatalf("GetResources returned error: %v", err)
	}
	if len(resources) != 0 {
		t.Fatalf("expected no resources, got %d", len(resources))
	}
}

func TestDNSResourcesPropagatesListerError(t *testing.T) {
	expectedErr := errors.New("projection unavailable")
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{
		DNSEndpoints: dnsEndpointListerFunc(func(ctx context.Context) ([]domain.DNSEndpoint, error) {
			return nil, expectedErr
		}),
	})

	_, err := server.GetResources(context.Background())
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected wrapped lister error, got %v", err)
	}
}
