package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/pashagolub/pgxmock/v5"
)

func TestPgSecretRepositoryCreateWritesVersionRow(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	repo := newPgSecretRepositoryWithDB(mock)
	secret := &domain.ServiceSecret{ID: uuid.New(), ServiceID: uuid.New(), Name: "TOKEN", EncryptedValue: []byte("ciphertext"), EncryptionMethod: domain.EncryptionMethod("aes256gcm"), Version: 1, CreatedBy: "operator"}

	mock.ExpectExec("WITH inserted AS").
		WithArgs(secret.ID, secret.ServiceID, secret.EnvironmentID, secret.Name, secret.EncryptedValue, string(secret.EncryptionMethod), secret.Version, secret.CreatedBy).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	if err := repo.Create(ctx, secret); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPgSecretRepositoryRecordSecretAccessAudit(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	repo := newPgSecretRepositoryWithDB(mock)
	envID := uuid.New()
	audit := &domain.SecretAccessAudit{
		ID: uuid.New(), SecretID: uuid.New(), VersionID: uuid.New(), Version: 3,
		ServiceID: uuid.New(), EnvironmentID: &envID, Operation: domain.SecretAccessOperationRuntimeApply,
		Outcome: domain.SecretAccessOutcomeFailure, Actor: "agent", Reason: "deploy", RequestID: "req-1", Error: "runtime rejected", AccessedAt: time.Now().UTC(),
	}

	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO secret_access_audit")).
		WithArgs(audit.ID, audit.SecretID, &audit.VersionID, audit.Version, audit.ServiceID, audit.EnvironmentID, string(audit.Operation), string(audit.Outcome), audit.Actor, audit.Reason, audit.RequestID, audit.Error, audit.AccessedAt).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	if err := repo.RecordSecretAccessAudit(ctx, audit); err != nil {
		t.Fatalf("RecordSecretAccessAudit: %v", err)
	}

	// A store-backed resolve has no PostgreSQL version row: NULL.
	storeBacked := *audit
	storeBacked.ID, storeBacked.VersionID = uuid.New(), uuid.Nil
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO secret_access_audit")).
		WithArgs(storeBacked.ID, storeBacked.SecretID, (*uuid.UUID)(nil), storeBacked.Version, storeBacked.ServiceID, storeBacked.EnvironmentID, string(storeBacked.Operation), string(storeBacked.Outcome), storeBacked.Actor, storeBacked.Reason, storeBacked.RequestID, storeBacked.Error, storeBacked.AccessedAt).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	if err := repo.RecordSecretAccessAudit(ctx, &storeBacked); err != nil {
		t.Fatalf("RecordSecretAccessAudit without a version row: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
