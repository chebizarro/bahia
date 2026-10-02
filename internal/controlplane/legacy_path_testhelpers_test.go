package controlplane

import (
	"context"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/secrets"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
)

// --- Service repository stub (satisfies repository.ServiceRepository) ---

type testSecretFixtureService struct {
	orgID uuid.UUID
}

func (r *testSecretFixtureService) Create(_ context.Context, _ *domain.Service) error { return nil }
func (r *testSecretFixtureService) GetByID(_ context.Context, _ uuid.UUID) (*domain.Service, error) {
	return &domain.Service{OrgID: r.orgID}, nil
}
func (r *testSecretFixtureService) GetByName(_ context.Context, _ string) (*domain.Service, error) {
	return nil, nil
}
func (r *testSecretFixtureService) List(_ context.Context) ([]domain.Service, error) {
	return nil, nil
}
func (r *testSecretFixtureService) ListByOrg(_ context.Context, _ uuid.UUID) ([]domain.Service, error) {
	return nil, nil
}
func (r *testSecretFixtureService) Update(_ context.Context, _ *domain.Service) error { return nil }
func (r *testSecretFixtureService) Delete(_ context.Context, _ uuid.UUID) error       { return nil }

// --- Extended secret repo wrapper (adds missing interface methods to memSecretRepo) ---

type fullSecretRepo struct {
	*memSecretRepo
}

func (r *fullSecretRepo) GetCurrentVersion(_ context.Context, _ uuid.UUID) (*domain.SecretVersion, error) {
	return nil, nil
}
func (r *fullSecretRepo) ListByServiceAndEnv(_ context.Context, _, _ uuid.UUID) ([]domain.ServiceSecret, error) {
	return nil, nil
}
func (r *fullSecretRepo) ListEffective(_ context.Context, _, _ uuid.UUID) ([]domain.ServiceSecret, error) {
	return nil, nil
}
func (r *fullSecretRepo) RecordSecretAccessAudit(_ context.Context, _ *domain.SecretAccessAudit) error {
	return nil
}
func (r *fullSecretRepo) DeleteByName(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string) error {
	return nil
}

// --- Notification repo wrapper (adds log methods to memNotificationRepo) ---

type fullNotifRepo struct {
	*memNotificationRepo
}

func (r *fullNotifRepo) CreateLog(_ context.Context, _ *domain.NotificationLog) error { return nil }
func (r *fullNotifRepo) UpdateLog(_ context.Context, _ *domain.NotificationLog) error { return nil }
func (r *fullNotifRepo) ListLogsByChannel(_ context.Context, _ uuid.UUID, _ int) ([]domain.NotificationLog, error) {
	return nil, nil
}
func (r *fullNotifRepo) ListRecentLogs(_ context.Context, _ int) ([]domain.NotificationLog, error) {
	return nil, nil
}
func (r *fullNotifRepo) ListRetryable(_ context.Context, _ int) ([]domain.NotificationLog, error) {
	return nil, nil
}

// --- RBAC helpers ---

func testSecretLegacyRBAC(orgID uuid.UUID, pubkey string) *auth.RBAC {
	return auth.NewRBAC(&encryptedMemberRepo{
		members: []domain.OrgMember{{OrgID: orgID, Pubkey: pubkey, Role: domain.RoleAdmin}},
	})
}

// testPrivateKey is a deterministic 32-byte hex key for test encryption.
const testPrivateKey = "1111111111111111111111111111111111111111111111111111111111111111"

func testLegacyEncryptor() *secrets.Encryptor {
	enc, err := secrets.NewEncryptor(testPrivateKey)
	if err != nil {
		panic("test encryptor: " + err.Error())
	}
	return enc
}
