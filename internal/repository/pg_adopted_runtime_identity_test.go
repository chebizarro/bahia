package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPgAdoptedRuntimeIdentityRepository_UpsertManyUsesOneTenantScopedStatement(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgAdoptedRuntimeIdentityRepositoryWithDB(mock)
	orgID := uuid.New()
	identities := []domain.AdoptedRuntimeIdentity{
		{OrgID: orgID, ServiceID: uuid.New(), EnvironmentID: uuid.New(), FingerprintKind: "container_id", Fingerprint: "container|a"},
		{OrgID: orgID, ServiceID: uuid.New(), EnvironmentID: uuid.New(), FingerprintKind: "endpoint_target", Fingerprint: "endpoint|a"},
	}

	mock.ExpectExec("ON CONFLICT \\(org_id, fingerprint\\) DO UPDATE").
		WithArgs(adoptedIdentityAnyArgs(28)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 2))

	require.NoError(t, repo.UpsertMany(context.Background(), identities))
	require.NotEqual(t, uuid.Nil, identities[0].ID)
	require.NotEqual(t, uuid.Nil, identities[1].ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgAdoptedRuntimeIdentityRepository_UpsertManyDoesNotExposePartialPreparation(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgAdoptedRuntimeIdentityRepositoryWithDB(mock)
	identities := []domain.AdoptedRuntimeIdentity{{
		OrgID: uuid.New(), ServiceID: uuid.New(), EnvironmentID: uuid.New(), FingerprintKind: "container_id", Fingerprint: "container|a",
	}}
	mock.ExpectExec("INSERT INTO adopted_runtime_identity").
		WithArgs(adoptedIdentityAnyArgs(14)...).
		WillReturnError(errors.New("write failed"))

	require.Error(t, repo.UpsertMany(context.Background(), identities))
	require.Equal(t, uuid.Nil, identities[0].ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func adoptedIdentityAnyArgs(count int) []any {
	args := make([]any, count)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}

func TestPgAdoptedRuntimeIdentityRepository_FindByFingerprintsScopesOrganization(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgAdoptedRuntimeIdentityRepositoryWithDB(mock)
	orgID := uuid.New()
	fingerprints := []string{"container|a"}
	mock.ExpectQuery("WHERE org_id = \\$1 AND fingerprint = ANY\\(\\$2\\)").
		WithArgs(orgID, fingerprints).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "service_id", "environment_id", "fingerprint_kind", "fingerprint", "container_id", "image_digest", "endpoint_ref", "host_alias", "target_name", "compose", "created_at", "updated_at"}))

	got, err := repo.FindByFingerprints(context.Background(), orgID, fingerprints)
	require.NoError(t, err)
	require.Empty(t, got)
	require.NoError(t, mock.ExpectationsWereMet())
}
