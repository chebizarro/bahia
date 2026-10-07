package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/pashagolub/pgxmock/v5"
	"github.com/stretchr/testify/require"
)

// Client-minted ids (bahia-irsry.35) are stored verbatim; the database never
// generates them.
func TestPgServiceCreateStoresClientSuppliedID(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	id := domain.NewEntityID()
	mock.ExpectExec("INSERT INTO services").
		WithArgs(id, pgxmock.AnyArg(), "api", pgxmock.AnyArg(), pgxmock.AnyArg(), "ghcr.io/acme/api", "main", domain.RuntimeTypeDocker, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	svc := &domain.Service{ID: id, Name: "api", ArtifactRepo: "ghcr.io/acme/api", DefaultBranch: "main", RuntimeType: domain.RuntimeTypeDocker}
	require.NoError(t, newPgServiceRepositoryWithDB(mock).Create(context.Background(), svc))
	require.Equal(t, id, svc.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgServiceCreateMintsUUIDv7WhenIDAbsent(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	mock.ExpectExec("INSERT INTO services").WithArgs(anyArgs(11)...).WillReturnResult(pgxmock.NewResult("INSERT", 1))

	svc := &domain.Service{Name: "api", ArtifactRepo: "ghcr.io/acme/api", RuntimeType: domain.RuntimeTypeDocker}
	require.NoError(t, newPgServiceRepositoryWithDB(mock).Create(context.Background(), svc))
	require.EqualValues(t, 7, svc.ID.Version())
}

func TestPgServiceCreateClassifiesUniqueViolations(t *testing.T) {
	for _, tc := range []struct {
		constraint string
		want       error
	}{
		{"services_pkey", ErrAlreadyExists},
		{"services_name_key", ErrConflict},
	} {
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		mock.ExpectExec("INSERT INTO services").WithArgs(anyArgs(11)...).WillReturnError(&pgconn.PgError{Code: "23505", ConstraintName: tc.constraint})
		err = newPgServiceRepositoryWithDB(mock).Create(context.Background(), &domain.Service{ID: domain.NewEntityID(), Name: "api"})
		require.Truef(t, errors.Is(err, tc.want), "constraint %s: got %v, want %v", tc.constraint, err, tc.want)
		mock.Close()
	}
}

func TestPgEnvironmentCreateClassifiesPrimaryKeyViolation(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	id := domain.NewEntityID()
	mock.ExpectExec("INSERT INTO environments").
		WithArgs(id, pgxmock.AnyArg(), "prod", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(&pgconn.PgError{Code: "23505", ConstraintName: "environments_pkey"})

	err = newPgEnvironmentRepositoryWithDB(mock).Create(context.Background(), &domain.Environment{ID: id, Name: "prod"})
	require.ErrorIs(t, err, ErrAlreadyExists)
	require.Contains(t, err.Error(), id.String())
}
