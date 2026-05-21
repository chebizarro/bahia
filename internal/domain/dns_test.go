package domain

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestDNSEnumsIsValid(t *testing.T) {
	if !DNSBackendTypeFilesystem.IsValid() || DNSBackendType("bogus").IsValid() {
		t.Fatalf("DNSBackendType IsValid mismatch")
	}
	if !ZoneVisibilityInternal.IsValid() || ZoneVisibility("public").IsValid() {
		t.Fatalf("ZoneVisibility IsValid mismatch")
	}
	if !DNSRecordTypeA.IsValid() || DNSRecordType("TXT").IsValid() {
		t.Fatalf("DNSRecordType IsValid mismatch")
	}
	if !DNSEndpointFamilyService.IsValid() || DNSEndpointFamily("database").IsValid() {
		t.Fatalf("DNSEndpointFamily IsValid mismatch")
	}
}

func TestDNSEndpointDTag(t *testing.T) {
	endpoint := DNSEndpoint{Family: DNSEndpointFamilyService, Name: "api", Environment: "prod"}
	if got, want := endpoint.DTag(), "endpoint:service:api:prod"; got != want {
		t.Fatalf("DTag() = %q, want %q", got, want)
	}

	worker := DNSEndpoint{Family: DNSEndpointFamilyWorker, Name: "t7920-l40s", Environment: "ignored"}
	if got, want := worker.DTag(), "endpoint:worker:t7920-l40s"; got != want {
		t.Fatalf("worker DTag() = %q, want %q", got, want)
	}
}

func TestDeterministicDNSEndpointID(t *testing.T) {
	dTag := "endpoint:service:api:prod"
	first := DeterministicDNSEndpointID(dTag)
	second := DeterministicDNSEndpointID(dTag)
	if first != second {
		t.Fatalf("deterministic IDs differ: %s != %s", first, second)
	}
	if first == uuid.Nil {
		t.Fatalf("deterministic ID must not be nil")
	}
	if first.Version() != 5 {
		t.Fatalf("deterministic ID version = %d, want 5", first.Version())
	}
}

func TestValidateDNSEndpoint(t *testing.T) {
	endpoint := &DNSEndpoint{
		Family:      DNSEndpointFamilyLLM,
		Name:        "review",
		Environment: "prod",
		Zone:        "prod.cascadia",
		FQDN:        "review.prod.cascadia",
		Address:     "10.0.1.44",
		Health:      HealthStatusHealthy,
		DriftStatus: DriftStatusInSync,
		Source:      "test",
	}
	if err := ValidateDNSEndpoint(endpoint); err != nil {
		t.Fatalf("ValidateDNSEndpoint() error = %v", err)
	}
	if endpoint.Coordinate != "endpoint:llm:review:prod" {
		t.Fatalf("Coordinate = %q", endpoint.Coordinate)
	}
	if endpoint.ID != DeterministicDNSEndpointID(endpoint.Coordinate) {
		t.Fatalf("ID was not derived from coordinate")
	}

	invalid := *endpoint
	invalid.Coordinate = "endpoint:llm:other:prod"
	if err := ValidateDNSEndpoint(&invalid); err == nil || !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("expected invalid coordinate error, got %v", err)
	}

	worker := &DNSEndpoint{
		Family:      DNSEndpointFamilyWorker,
		Name:        "worker-a",
		Zone:        "edge.cascadia",
		FQDN:        "worker-a.edge.cascadia",
		Address:     "worker-a.local",
		Health:      HealthStatusHealthy,
		DriftStatus: DriftStatusInSync,
		Source:      "test",
	}
	if err := ValidateDNSEndpoint(worker); err != nil {
		t.Fatalf("worker endpoint should not require environment: %v", err)
	}
}

func TestValidateDNSZone(t *testing.T) {
	zone := &DNSZone{Name: "prod.cascadia", Visibility: ZoneVisibilityInternal, BackendRef: "fs", TTL: 300}
	if err := ValidateDNSZone(zone); err != nil {
		t.Fatalf("ValidateDNSZone() error = %v", err)
	}

	invalidVisibility := *zone
	invalidVisibility.Visibility = ZoneVisibility("private")
	if err := ValidateDNSZone(&invalidVisibility); err == nil || !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("expected invalid visibility error, got %v", err)
	}

	invalidTTL := *zone
	invalidTTL.TTL = 0
	if err := ValidateDNSZone(&invalidTTL); err == nil || !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("expected invalid TTL error, got %v", err)
	}
}
