package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// DNSPolicyRepository manages persisted DNS policy records.
type DNSPolicyRepository interface {
	Create(ctx context.Context, policy *domain.DNSPolicy) error
	Get(ctx context.Context, id uuid.UUID) (*domain.DNSPolicy, error)
	List(ctx context.Context) ([]domain.DNSPolicy, error)
	ListEnabled(ctx context.Context) ([]domain.DNSPolicy, error)
	Update(ctx context.Context, policy *domain.DNSPolicy) error
	Delete(ctx context.Context, id uuid.UUID) error
}

// DNSZoneRepository manages persisted DNS zone records.
type DNSZoneRepository interface {
	Create(ctx context.Context, zone *domain.DNSZone) error
	Get(ctx context.Context, name string) (*domain.DNSZone, error)
	List(ctx context.Context) ([]domain.DNSZone, error)
	Delete(ctx context.Context, name string) error
}

// DNSRecordOverrideRepository manages manual DNS record pins.
type DNSRecordOverrideRepository interface {
	Create(ctx context.Context, override *domain.DNSRecordOverride) error
	Get(ctx context.Context, id uuid.UUID) (*domain.DNSRecordOverride, error)
	ListByZone(ctx context.Context, zoneName string) ([]domain.DNSRecordOverride, error)
	Delete(ctx context.Context, id uuid.UUID) error
}
