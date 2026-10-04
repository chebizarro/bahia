package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

type DNSMutationPublisher interface {
	PublishZone(context.Context, domain.DNSZone) error
	PublishZoneTombstone(context.Context, string) error
	PublishPolicy(context.Context, domain.DNSPolicy) error
	PublishPolicyTombstone(context.Context, uuid.UUID) error
	PublishEndpoint(context.Context, domain.DNSEndpoint) error
	PublishEndpointTombstone(context.Context, domain.DNSEndpoint) error
	PublishBackend(context.Context, domain.DNSBackendState) error
	PublishBackendTombstone(context.Context, string) error
}

type DNSMutationReconciler interface {
	ReconcileAll(context.Context) error
	RetireZone(context.Context, domain.DNSZone) error
	HasBackend(string) bool
}

type DNSMutationService struct {
	Zones      repository.DNSZoneRepository
	Policies   repository.DNSPolicyRepository
	Endpoints  repository.DNSEndpointRepository
	Backends   repository.DNSBackendRepository
	Canonical  DNSMutationPublisher
	Reconciler DNSMutationReconciler
}

func (s *DNSMutationService) GetZone(ctx context.Context, name string) (*domain.DNSZone, error) {
	return s.Zones.Get(ctx, name)
}
func (s *DNSMutationService) GetPolicy(ctx context.Context, id uuid.UUID) (*domain.DNSPolicy, error) {
	return s.Policies.Get(ctx, id)
}
func (s *DNSMutationService) GetEndpoint(ctx context.Context, coordinate string) (*domain.DNSEndpoint, error) {
	return s.Endpoints.Get(ctx, coordinate)
}
func (s *DNSMutationService) GetBackend(ctx context.Context, ref string) (*domain.DNSBackendState, error) {
	return s.Backends.Get(ctx, ref)
}

func (s *DNSMutationService) validateZone(ctx context.Context, zone *domain.DNSZone) error {
	if err := domain.ValidateDNSZone(zone); err != nil {
		return err
	}
	registered, err := s.Backends.Get(ctx, zone.BackendRef)
	if err != nil {
		return err
	}
	if registered == nil {
		return fmt.Errorf("DNS backend %q is not registered", zone.BackendRef)
	}
	if !s.Reconciler.HasBackend(zone.BackendRef) {
		return fmt.Errorf("DNS backend %q is not configured", zone.BackendRef)
	}
	return nil
}

func (s *DNSMutationService) CreateZone(ctx context.Context, zone *domain.DNSZone) error {
	if err := s.validateZone(ctx, zone); err != nil {
		return err
	}
	if err := s.Zones.Create(ctx, zone); err != nil {
		return err
	}
	s.setZoneActive(zone.Name, true)
	if err := s.Reconciler.ReconcileAll(ctx); err != nil {
		return err
	}
	return s.Canonical.PublishZone(ctx, *zone)
}
func (s *DNSMutationService) UpdateZone(ctx context.Context, zone *domain.DNSZone) error {
	if err := s.validateZone(ctx, zone); err != nil {
		return err
	}
	if err := s.Zones.Update(ctx, zone); err != nil {
		return err
	}
	s.setZoneActive(zone.Name, true)
	if err := s.Reconciler.ReconcileAll(ctx); err != nil {
		return err
	}
	return s.Canonical.PublishZone(ctx, *zone)
}
func (s *DNSMutationService) DeleteZone(ctx context.Context, name string) error {
	zone, err := s.Zones.Get(ctx, name)
	if err != nil {
		return err
	}
	if zone == nil {
		return fmt.Errorf("DNS zone %q: %w", name, repository.ErrNotFound)
	}
	if err := s.Reconciler.RetireZone(ctx, *zone); err != nil {
		return err
	}
	endpoints, err := s.Endpoints.List(ctx)
	if err != nil {
		return err
	}
	for _, endpoint := range endpoints {
		if endpoint.Zone != name {
			continue
		}
		if err := s.Endpoints.Delete(ctx, endpoint.Coordinate); err != nil {
			return err
		}
		if err := s.Canonical.PublishEndpointTombstone(ctx, endpoint); err != nil {
			return err
		}
	}
	if err := s.Zones.Delete(ctx, name); err != nil {
		return err
	}
	s.setZoneActive(name, false)
	if err := s.Reconciler.ReconcileAll(ctx); err != nil {
		return err
	}
	return s.Canonical.PublishZoneTombstone(ctx, name)
}

func (s *DNSMutationService) setZoneActive(name string, active bool) {
	if operator, ok := s.Reconciler.(interface{ SetZoneActive(string, bool) }); ok {
		operator.SetZoneActive(name, active)
	}
}

func (s *DNSMutationService) CreatePolicy(ctx context.Context, policy *domain.DNSPolicy) error {
	if err := domain.ValidateDNSPolicy(policy); err != nil {
		return err
	}
	if err := s.Policies.Create(ctx, policy); err != nil {
		return err
	}
	if err := s.Reconciler.ReconcileAll(ctx); err != nil {
		return err
	}
	return s.Canonical.PublishPolicy(ctx, *policy)
}
func (s *DNSMutationService) UpdatePolicy(ctx context.Context, policy *domain.DNSPolicy) error {
	if err := domain.ValidateDNSPolicy(policy); err != nil {
		return err
	}
	if err := s.Policies.Update(ctx, policy); err != nil {
		return err
	}
	if err := s.Reconciler.ReconcileAll(ctx); err != nil {
		return err
	}
	return s.Canonical.PublishPolicy(ctx, *policy)
}
func (s *DNSMutationService) DeletePolicy(ctx context.Context, id uuid.UUID) error {
	if err := s.Policies.Delete(ctx, id); err != nil {
		return err
	}
	if err := s.Reconciler.ReconcileAll(ctx); err != nil {
		return err
	}
	return s.Canonical.PublishPolicyTombstone(ctx, id)
}

func (s *DNSMutationService) UpsertEndpoint(ctx context.Context, endpoint *domain.DNSEndpoint) error {
	if err := domain.ValidateDNSEndpoint(endpoint); err != nil {
		return err
	}
	zone, err := s.Zones.Get(ctx, endpoint.Zone)
	if err != nil {
		return err
	}
	if zone == nil {
		return fmt.Errorf("DNS zone %q: %w", endpoint.Zone, repository.ErrNotFound)
	}
	if err := s.Endpoints.Upsert(ctx, endpoint); err != nil {
		return err
	}
	if err := s.Canonical.PublishEndpoint(ctx, *endpoint); err != nil {
		return err
	}
	return s.Reconciler.ReconcileAll(ctx)
}
func (s *DNSMutationService) DeleteEndpoint(ctx context.Context, coordinate string) error {
	endpoint, err := s.Endpoints.Get(ctx, coordinate)
	if err != nil {
		return err
	}
	if endpoint == nil {
		return fmt.Errorf("DNS endpoint %q: %w", coordinate, repository.ErrNotFound)
	}
	if err := s.Endpoints.Delete(ctx, coordinate); err != nil {
		return err
	}
	if err := s.Canonical.PublishEndpointTombstone(ctx, *endpoint); err != nil {
		return err
	}
	return s.Reconciler.ReconcileAll(ctx)
}

func (s *DNSMutationService) UpsertBackend(ctx context.Context, backend *domain.DNSBackendState) error {
	backend.Ref = strings.TrimSpace(backend.Ref)
	if backend.Ref == "" || !backend.Type.IsValid() {
		return fmt.Errorf("DNS backend ref and valid type are required")
	}
	if err := s.Backends.Upsert(ctx, backend); err != nil {
		return err
	}
	return s.Canonical.PublishBackend(ctx, *backend)
}
func (s *DNSMutationService) DeleteBackend(ctx context.Context, ref string) error {
	zones, err := s.Zones.List(ctx)
	if err != nil {
		return err
	}
	for _, zone := range zones {
		if zone.BackendRef == ref {
			return fmt.Errorf("DNS backend %q is used by zone %q", ref, zone.Name)
		}
	}
	if err := s.Backends.Delete(ctx, ref); err != nil {
		return err
	}
	return s.Canonical.PublishBackendTombstone(ctx, ref)
}
