package secrets

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

type auditSecretRepo struct {
	secret  *domain.ServiceSecret
	version *domain.SecretVersion
	audits  []*domain.SecretAccessAudit
}

func (r *auditSecretRepo) Create(context.Context, *domain.ServiceSecret) error { return nil }
func (r *auditSecretRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.ServiceSecret, error) {
	if r.secret != nil && r.secret.ID == id {
		copy := *r.secret
		return &copy, nil
	}
	return nil, nil
}
func (r *auditSecretRepo) GetCurrentVersion(_ context.Context, secretID uuid.UUID) (*domain.SecretVersion, error) {
	if r.version != nil && r.version.SecretID == secretID {
		copy := *r.version
		return &copy, nil
	}
	return nil, nil
}
func (r *auditSecretRepo) ListByService(context.Context, uuid.UUID) ([]domain.ServiceSecret, error) {
	return nil, nil
}
func (r *auditSecretRepo) ListByServiceAndEnv(context.Context, uuid.UUID, uuid.UUID) ([]domain.ServiceSecret, error) {
	return nil, nil
}
func (r *auditSecretRepo) ListEffective(context.Context, uuid.UUID, uuid.UUID) ([]domain.ServiceSecret, error) {
	return nil, nil
}
func (r *auditSecretRepo) Update(context.Context, *domain.ServiceSecret) error { return nil }
func (r *auditSecretRepo) RecordSecretAccessAudit(_ context.Context, audit *domain.SecretAccessAudit) error {
	copy := *audit
	r.audits = append(r.audits, &copy)
	return nil
}
func (r *auditSecretRepo) Delete(context.Context, uuid.UUID) error { return nil }
func (r *auditSecretRepo) DeleteByName(context.Context, uuid.UUID, *uuid.UUID, string) error {
	return nil
}

func TestResolverResolveSecretWithAuditRecordsVersionedAccess(t *testing.T) {
	encryptor, err := NewEncryptor("test-secret-key")
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	ciphertext, err := encryptor.Encrypt("super-secret", domain.EncryptionAES256)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	secretID := uuid.New()
	versionID := uuid.New()
	serviceID := uuid.New()
	envID := uuid.New()
	repo := &auditSecretRepo{
		secret:  &domain.ServiceSecret{ID: secretID, ServiceID: serviceID, EnvironmentID: &envID, Name: "DB_PASSWORD", Version: 2},
		version: &domain.SecretVersion{ID: versionID, SecretID: secretID, Version: 2, EncryptedValue: ciphertext, EncryptionMethod: domain.EncryptionAES256, CreatedBy: "operator"},
	}

	value, manifest, err := NewResolver(repo, encryptor).ResolveSecretWithAudit(context.Background(), secretID.String(), domain.SecretResolveOptions{Actor: "agent", Reason: "deploy", RequestID: "req-1"})
	if err != nil {
		t.Fatalf("ResolveSecretWithAudit: %v", err)
	}
	if value != "super-secret" {
		t.Fatalf("resolved value = %q", value)
	}
	if manifest.SecretID != secretID || manifest.VersionID != versionID || manifest.Version != 2 || manifest.Name != "DB_PASSWORD" {
		t.Fatalf("unexpected manifest: %#v", manifest)
	}
	if manifest.Outcome != domain.SecretAccessOutcomeSuccess || manifest.Operation != domain.SecretAccessOperationResolve {
		t.Fatalf("unexpected manifest outcome/operation: %#v", manifest)
	}
	if len(repo.audits) != 1 {
		t.Fatalf("audit count = %d, want 1", len(repo.audits))
	}
	audit := repo.audits[0]
	if audit.SecretID != secretID || audit.VersionID != versionID || audit.Version != 2 || audit.ServiceID != serviceID || audit.EnvironmentID == nil || *audit.EnvironmentID != envID {
		t.Fatalf("unexpected audit coordinates: %#v", audit)
	}
	if audit.Actor != "agent" || audit.Reason != "deploy" || audit.RequestID != "req-1" || audit.Error != "" {
		t.Fatalf("unexpected safe audit metadata: %#v", audit)
	}
}

func TestResolverResolveSecretWithAuditAuditsDecryptFailureWithoutPlaintext(t *testing.T) {
	encryptor, err := NewEncryptor("test-secret-key")
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	secretID := uuid.New()
	versionID := uuid.New()
	repo := &auditSecretRepo{
		secret:  &domain.ServiceSecret{ID: secretID, ServiceID: uuid.New(), Name: "API_KEY", Version: 1},
		version: &domain.SecretVersion{ID: versionID, SecretID: secretID, Version: 1, EncryptedValue: []byte("not-a-valid-ciphertext"), EncryptionMethod: domain.EncryptionAES256, CreatedBy: "operator"},
	}

	_, manifest, err := NewResolver(repo, encryptor).ResolveSecretWithAudit(context.Background(), secretID.String(), domain.SecretResolveOptions{})
	if err == nil {
		t.Fatal("expected decrypt error")
	}
	if manifest.Outcome != domain.SecretAccessOutcomeFailure {
		t.Fatalf("manifest outcome = %q", manifest.Outcome)
	}
	if len(repo.audits) != 1 {
		t.Fatalf("audit count = %d, want 1", len(repo.audits))
	}
	if repo.audits[0].Outcome != domain.SecretAccessOutcomeFailure || repo.audits[0].Error == "" {
		t.Fatalf("expected failed audit with safe error, got %#v", repo.audits[0])
	}
}
