package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
)

type localDNSRecords struct{ store *localstore.Outbox }

func (r localDNSRecords) get(family, id string, value any) (bool, error) {
	data, err := r.store.GetControlRecord(family, id)
	if err != nil || data == nil {
		return false, err
	}
	return true, json.Unmarshal(data, value)
}

func (r localDNSRecords) put(family, id string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return r.store.PutControlRecord(family, id, data)
}

func (r localDNSRecords) list(family string, target any) error {
	data, err := r.store.ListControlRecords(family)
	if err != nil {
		return err
	}
	items := make([]json.RawMessage, 0, len(data))
	for _, item := range data {
		items = append(items, item)
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, target)
}

type LocalDNSZoneRepository struct{ localDNSRecords }

func NewLocalDNSZoneRepository(store *localstore.Outbox) *LocalDNSZoneRepository {
	return &LocalDNSZoneRepository{localDNSRecords{store}}
}
func (r *LocalDNSZoneRepository) Create(ctx context.Context, zone *domain.DNSZone) error {
	return r.upsert(ctx, zone)
}
func (r *LocalDNSZoneRepository) SeedConfigured(ctx context.Context, zone *domain.DNSZone) error {
	deleted, err := r.store.GetControlRecord("dns-zone-deleted", zone.Name)
	if err != nil || deleted != nil {
		return err
	}
	existing, err := r.Get(ctx, zone.Name)
	if err != nil || existing != nil {
		return err
	}
	return r.Create(ctx, zone)
}
func (r *LocalDNSZoneRepository) Update(ctx context.Context, zone *domain.DNSZone) error {
	current, err := r.Get(ctx, zone.Name)
	if err != nil {
		return err
	}
	if current == nil {
		return fmt.Errorf("DNS zone %q: %w", zone.Name, ErrNotFound)
	}
	return r.upsert(ctx, zone)
}
func (r *LocalDNSZoneRepository) upsert(_ context.Context, zone *domain.DNSZone) error {
	zone.UpdatedAt = time.Now().UTC()
	return r.put("dns-zone", zone.Name, zone)
}
func (r *LocalDNSZoneRepository) Get(_ context.Context, name string) (*domain.DNSZone, error) {
	var value domain.DNSZone
	ok, err := r.get("dns-zone", name, &value)
	if !ok || err != nil {
		return nil, err
	}
	return &value, nil
}
func (r *LocalDNSZoneRepository) List(context.Context) ([]domain.DNSZone, error) {
	var values []domain.DNSZone
	err := r.list("dns-zone", &values)
	return values, err
}
func (r *LocalDNSZoneRepository) Delete(_ context.Context, name string) error {
	found, err := r.store.DeleteControlRecordWithMarker("dns-zone", name, "dns-zone-deleted", []byte("1"))
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("DNS zone %q: %w", name, ErrNotFound)
	}
	return nil
}

type LocalDNSPolicyRepository struct{ localDNSRecords }

func NewLocalDNSPolicyRepository(store *localstore.Outbox) *LocalDNSPolicyRepository {
	return &LocalDNSPolicyRepository{localDNSRecords{store}}
}
func (r *LocalDNSPolicyRepository) Create(_ context.Context, policy *domain.DNSPolicy) error {
	if policy.ID == uuid.Nil {
		policy.ID = domain.NewEntityID()
	}
	now := time.Now().UTC()
	if policy.CreatedAt.IsZero() {
		policy.CreatedAt = now
	}
	policy.UpdatedAt = now
	return r.put("dns-policy", policy.ID.String(), policy)
}
func (r *LocalDNSPolicyRepository) Update(ctx context.Context, policy *domain.DNSPolicy) error {
	current, err := r.Get(ctx, policy.ID)
	if err != nil {
		return err
	}
	if current == nil {
		return fmt.Errorf("DNS policy %s: %w", policy.ID, ErrNotFound)
	}
	policy.CreatedAt = current.CreatedAt
	policy.UpdatedAt = time.Now().UTC()
	return r.put("dns-policy", policy.ID.String(), policy)
}
func (r *LocalDNSPolicyRepository) Get(_ context.Context, id uuid.UUID) (*domain.DNSPolicy, error) {
	var value domain.DNSPolicy
	ok, err := r.get("dns-policy", id.String(), &value)
	if !ok || err != nil {
		return nil, err
	}
	return &value, nil
}
func (r *LocalDNSPolicyRepository) List(context.Context) ([]domain.DNSPolicy, error) {
	var values []domain.DNSPolicy
	err := r.list("dns-policy", &values)
	return values, err
}
func (r *LocalDNSPolicyRepository) ListEnabled(ctx context.Context) ([]domain.DNSPolicy, error) {
	all, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	var enabled []domain.DNSPolicy
	for _, policy := range all {
		if policy.Enabled {
			enabled = append(enabled, policy)
		}
	}
	return enabled, nil
}
func (r *LocalDNSPolicyRepository) ListEnabledPolicies(ctx context.Context) ([]domain.DNSPolicy, error) {
	return r.ListEnabled(ctx)
}
func (r *LocalDNSPolicyRepository) Delete(_ context.Context, id uuid.UUID) error {
	found, err := r.store.DeleteControlRecord("dns-policy", id.String())
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("DNS policy %s: %w", id, ErrNotFound)
	}
	return nil
}

type LocalDNSEndpointRepository struct{ localDNSRecords }

func NewLocalDNSEndpointRepository(store *localstore.Outbox) *LocalDNSEndpointRepository {
	return &LocalDNSEndpointRepository{localDNSRecords{store}}
}
func (r *LocalDNSEndpointRepository) Upsert(_ context.Context, endpoint *domain.DNSEndpoint) error {
	endpoint.UpdatedAt = time.Now().UTC()
	return r.put("dns-endpoint", endpoint.Coordinate, endpoint)
}
func (r *LocalDNSEndpointRepository) Get(_ context.Context, coordinate string) (*domain.DNSEndpoint, error) {
	var value domain.DNSEndpoint
	ok, err := r.get("dns-endpoint", coordinate, &value)
	if !ok || err != nil {
		return nil, err
	}
	return &value, nil
}
func (r *LocalDNSEndpointRepository) List(context.Context) ([]domain.DNSEndpoint, error) {
	var values []domain.DNSEndpoint
	err := r.list("dns-endpoint", &values)
	return values, err
}
func (r *LocalDNSEndpointRepository) Delete(_ context.Context, coordinate string) error {
	found, err := r.store.DeleteControlRecord("dns-endpoint", coordinate)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("DNS endpoint %q: %w", coordinate, ErrNotFound)
	}
	return nil
}

type LocalDNSBackendRepository struct{ localDNSRecords }

func NewLocalDNSBackendRepository(store *localstore.Outbox) *LocalDNSBackendRepository {
	return &LocalDNSBackendRepository{localDNSRecords{store}}
}
func (r *LocalDNSBackendRepository) SeedConfigured(ctx context.Context, backend *domain.DNSBackendState) error {
	deleted, err := r.store.GetControlRecord("dns-backend-deleted", backend.Ref)
	if err != nil || deleted != nil {
		return err
	}
	existing, err := r.Get(ctx, backend.Ref)
	if err != nil || existing != nil {
		return err
	}
	return r.Upsert(ctx, backend)
}
func (r *LocalDNSBackendRepository) Upsert(_ context.Context, backend *domain.DNSBackendState) error {
	backend.UpdatedAt = time.Now().UTC()
	return r.put("dns-backend", backend.Ref, backend)
}
func (r *LocalDNSBackendRepository) Get(_ context.Context, ref string) (*domain.DNSBackendState, error) {
	var value domain.DNSBackendState
	ok, err := r.get("dns-backend", ref, &value)
	if !ok || err != nil {
		return nil, err
	}
	return &value, nil
}
func (r *LocalDNSBackendRepository) List(context.Context) ([]domain.DNSBackendState, error) {
	var values []domain.DNSBackendState
	err := r.list("dns-backend", &values)
	return values, err
}
func (r *LocalDNSBackendRepository) Delete(_ context.Context, ref string) error {
	found, err := r.store.DeleteControlRecordWithMarker("dns-backend", ref, "dns-backend-deleted", []byte("1"))
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("DNS backend %q: %w", ref, ErrNotFound)
	}
	return nil
}

type LocalDNSRecordOverrideRepository struct{ localDNSRecords }

func NewLocalDNSRecordOverrideRepository(store *localstore.Outbox) *LocalDNSRecordOverrideRepository {
	return &LocalDNSRecordOverrideRepository{localDNSRecords{store}}
}
func (r *LocalDNSRecordOverrideRepository) Create(_ context.Context, override *domain.DNSRecordOverride) error {
	if override.ID == uuid.Nil {
		override.ID = domain.NewEntityID()
	}
	if override.CreatedAt.IsZero() {
		override.CreatedAt = time.Now().UTC()
	}
	return r.put("dns-override", override.ID.String(), override)
}
func (r *LocalDNSRecordOverrideRepository) Get(_ context.Context, id uuid.UUID) (*domain.DNSRecordOverride, error) {
	var value domain.DNSRecordOverride
	ok, err := r.get("dns-override", id.String(), &value)
	if !ok || err != nil {
		return nil, err
	}
	return &value, nil
}
func (r *LocalDNSRecordOverrideRepository) ListByZone(_ context.Context, zoneName string) ([]domain.DNSRecordOverride, error) {
	var values []domain.DNSRecordOverride
	if err := r.list("dns-override", &values); err != nil {
		return nil, err
	}
	var matching []domain.DNSRecordOverride
	now := time.Now().UTC()
	for _, override := range values {
		if override.ZoneName == zoneName && (override.ExpiresAt == nil || override.ExpiresAt.After(now)) {
			matching = append(matching, override)
		}
	}
	return matching, nil
}
func (r *LocalDNSRecordOverrideRepository) Delete(_ context.Context, id uuid.UUID) error {
	found, err := r.store.DeleteControlRecord("dns-override", id.String())
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("DNS override %s: %w", id, ErrNotFound)
	}
	return nil
}
func (r *LocalDNSRecordOverrideRepository) Expire(ctx context.Context, id uuid.UUID, at time.Time) error {
	override, err := r.Get(ctx, id)
	if err != nil {
		return err
	}
	if override == nil {
		return fmt.Errorf("DNS override %s: %w", id, ErrNotFound)
	}
	when := at.UTC()
	override.ExpiresAt = &when
	return r.put("dns-override", id.String(), override)
}
