package repository_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

// A secret moved to the secret store keeps a value-less marker row; every
// retained legacy ciphertext is scrubbed while access audit rows (which
// reference versions ON DELETE RESTRICT) stay intact; and deleting a secret
// that was ever resolved succeeds.
func TestPgSecretStoreMarkerScrubsVersionsAndKeepsAudit(t *testing.T) {
	pool := openIdentityTestPool(t)
	ctx := context.Background()
	repo := repository.NewPgSecretRepository(pool)
	serviceID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO services (id, name, artifact_repo) VALUES ($1, $2, 'registry.example/marker')`, serviceID, "marker-"+serviceID.String()[:8]); err != nil {
		t.Fatal(err)
	}
	secret := &domain.ServiceSecret{ID: uuid.New(), ServiceID: serviceID, Name: "DB_PASSWORD", EncryptedValue: []byte("sealed-v1"), EncryptionMethod: domain.EncryptionAES256, CreatedBy: "t"}
	if err := repo.Create(ctx, secret); err != nil {
		t.Fatal(err)
	}
	secret.EncryptedValue, secret.Version = []byte("sealed-v2"), 1
	if err := repo.Update(ctx, secret); err != nil {
		t.Fatal(err)
	}
	current, err := repo.GetCurrentVersion(ctx, secret.ID)
	if err != nil || current == nil {
		t.Fatalf("current version: %v", err)
	}
	if err := repo.RecordSecretAccessAudit(ctx, &domain.SecretAccessAudit{SecretID: secret.ID, VersionID: current.ID, Version: current.Version, ServiceID: serviceID,
		Operation: domain.SecretAccessOperationResolve, Outcome: domain.SecretAccessOutcomeSuccess, AccessedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	ref := domain.SecretRef{ID: secret.ID, ServiceID: serviceID, Name: "DB_PASSWORD", Version: 3, CreatedBy: "operator"}
	if err := repo.MarkStoreBacked(ctx, ref); err != nil {
		t.Fatalf("mark store-backed: %v", err)
	}
	row, err := repo.GetByID(ctx, secret.ID)
	if err != nil || row == nil || row.EncryptionMethod != domain.EncryptionStoreRef || len(row.EncryptedValue) != 0 || row.Version != 3 {
		t.Fatalf("marker row = %+v, %v", row, err)
	}
	versions, err := repo.ListVersions(ctx, secret.ID)
	if err != nil || len(versions) != 2 {
		t.Fatalf("versions = %d, %v", len(versions), err)
	}
	for _, v := range versions {
		if len(v.EncryptedValue) != 0 || v.EncryptionMethod != domain.EncryptionStoreRef {
			t.Fatalf("version %d still holds ciphertext (%s)", v.Version, v.EncryptionMethod)
		}
	}
	var audits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM secret_access_audit WHERE secret_id = $1`, secret.ID).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audit rows = %d, %v", audits, err)
	}
	// Idempotent, and a new store-backed id gets a marker without versions.
	if err := repo.MarkStoreBacked(ctx, ref); err != nil {
		t.Fatalf("re-mark: %v", err)
	}
	fresh := domain.SecretRef{ID: uuid.New(), ServiceID: serviceID, Name: "API_KEY", Version: 1, CreatedBy: "operator"}
	if err := repo.MarkStoreBacked(ctx, fresh); err != nil {
		t.Fatalf("mark new: %v", err)
	}
	// The row of one service is never re-labelled for another.
	other := domain.SecretRef{ID: secret.ID, ServiceID: uuid.New(), Name: "DB_PASSWORD", Version: 4, CreatedBy: "operator"}
	if err := repo.MarkStoreBacked(ctx, other); err == nil {
		t.Fatal("a marker moved a secret to another service")
	}

	// Deleting a secret with access audit rows removes the secret, its
	// versions and its audit rows.
	if err := repo.Delete(ctx, secret.ID); err != nil {
		t.Fatalf("delete a secret with audit rows: %v", err)
	}
	if row, _ := repo.GetByID(ctx, secret.ID); row != nil {
		t.Fatal("secret survived delete")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM secret_access_audit WHERE secret_id = $1`, secret.ID).Scan(&audits); err != nil || audits != 0 {
		t.Fatalf("audit rows after delete = %d, %v", audits, err)
	}
}

// A store-backed secret whose service, environment and name another row
// holds is refused without changing anything (service-wide NULL environments
// included, which the unique constraint does not see); blocking scrubs the
// other row so no consumer serves it.
func TestPgSecretStoreMarkerRefusesAndBlocksNameCollision(t *testing.T) {
	pool := openIdentityTestPool(t)
	ctx := context.Background()
	repo := repository.NewPgSecretRepository(pool)
	serviceID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO services (id, name, artifact_repo) VALUES ($1, $2, 'registry.example/collide')`, serviceID, "collide-"+serviceID.String()[:8]); err != nil {
		t.Fatal(err)
	}
	legacy := &domain.ServiceSecret{ID: uuid.New(), ServiceID: serviceID, Name: "DB_PASSWORD", EncryptedValue: []byte("sealed"), EncryptionMethod: domain.EncryptionAES256, CreatedBy: "t"}
	if err := repo.Create(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	stored := domain.SecretRef{ID: uuid.New(), ServiceID: serviceID, Name: "DB_PASSWORD", Version: 1, CreatedBy: "operator"}
	if err := repo.MarkStoreBacked(ctx, stored); !errors.Is(err, repository.ErrSecretNameTaken) {
		t.Fatalf("mark over a same-name row = %v", err)
	}
	if row, _ := repo.GetByID(ctx, legacy.ID); row == nil || string(row.EncryptedValue) != "sealed" {
		t.Fatal("a refused mark changed the other row")
	}
	if row, _ := repo.GetByID(ctx, stored.ID); row != nil {
		t.Fatal("a refused mark wrote a row")
	}

	blocked, err := repo.BlockSameNameSecrets(ctx, stored)
	if err != nil || len(blocked) != 1 || blocked[0] != legacy.ID {
		t.Fatalf("blocked = %v, %v", blocked, err)
	}
	effective, err := repo.ListEffective(ctx, serviceID, uuid.New())
	if err != nil || len(effective) != 1 || effective[0].EncryptionMethod != domain.EncryptionStoreRef || len(effective[0].EncryptedValue) != 0 {
		t.Fatalf("effective after blocking = %+v, %v", effective, err)
	}
	versions, err := repo.ListVersions(ctx, legacy.ID)
	if err != nil || len(versions) != 1 || len(versions[0].EncryptedValue) != 0 {
		t.Fatalf("blocked row's versions still hold ciphertext: %+v, %v", versions, err)
	}

	// Once the other secret is deleted the store-backed secret is indexed.
	if err := repo.Delete(ctx, legacy.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkStoreBacked(ctx, stored); err != nil {
		t.Fatalf("mark after the collision is resolved: %v", err)
	}
}
