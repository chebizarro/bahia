package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	pgxmock "github.com/pashagolub/pgxmock/v4"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestPgOCIRepository_EnsureAndGetRepository(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgOCIRepositoryForTests(mock)
	now := time.Now().UTC()

	mock.ExpectExec(regexp.QuoteMeta(`
		INSERT INTO oci_repositories (name)
		VALUES ($1)
		ON CONFLICT (name) DO NOTHING
	`)).WithArgs("acme/app").WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + ociRepoColumns + ` FROM oci_repositories WHERE name = $1`)).
		WithArgs("acme/app").
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "namespace", "description", "created_at", "updated_at"}).
			AddRow("repo-id", "acme/app", "acme", "app", now, now))

	got, err := repo.EnsureRepository(context.Background(), "acme/app")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "repo-id", got.ID)
	require.Equal(t, "acme/app", got.Name)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgOCIRepository_PutManifest_WithTag(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgOCIRepositoryForTests(mock)

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO oci_manifests").
		WithArgs("repo-id", "sha256:abc", "application/vnd.oci.image.manifest.v1+json", "", "", []byte("{}"), int64(2), []byte(`{"k":"v"}`)).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("manifest-id"))
	mock.ExpectExec("INSERT INTO oci_tags").
		WithArgs("repo-id", "latest", "manifest-id").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	err = repo.PutManifest(context.Background(), domain.OCIManifest{
		RepositoryID: "repo-id",
		Digest:       "sha256:abc",
		MediaType:    "application/vnd.oci.image.manifest.v1+json",
		Content:      []byte("{}"),
		SizeBytes:    2,
		Annotations:  map[string]string{"k": "v"},
	}, "latest")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgOCIRepository_GetManifestByTag_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgOCIRepositoryForTests(mock)

	mock.ExpectQuery("FROM oci_tags t").
		WithArgs("acme/app", "latest").
		WillReturnError(pgx.ErrNoRows)

	m, err := repo.GetManifestByTag(context.Background(), "acme/app", "latest")
	require.NoError(t, err)
	require.Nil(t, m)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgOCIRepository_BlobAndUploadCRUD(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgOCIRepositoryForTests(mock)
	now := time.Now().UTC()

	mock.ExpectQuery("SELECT EXISTS").WithArgs("acme/app", "sha256:blob").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))

	exists, err := repo.BlobExistsInRepo(context.Background(), "acme/app", "sha256:blob")
	require.NoError(t, err)
	require.True(t, exists)

	upload := domain.OCIBlobUpload{
		UploadID:     "u-1",
		RepositoryID: "repo-id",
		SpoolPath:    "/tmp/blob",
		State:        domain.OCIBlobUploadStatePending,
		OffsetBytes:  0,
		StartedAt:    now,
		UpdatedAt:    now,
		ExpiresAt:    now.Add(time.Hour),
	}
	mock.ExpectExec("INSERT INTO oci_blob_uploads").
		WithArgs(upload.UploadID, upload.RepositoryID, upload.SpoolPath, string(upload.State), upload.OffsetBytes, upload.Digest, upload.StartedAt, upload.UpdatedAt, upload.ExpiresAt).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	require.NoError(t, repo.CreateUpload(context.Background(), upload))

	mock.ExpectQuery("SELECT .* FROM oci_blob_uploads WHERE upload_id = \\$1").
		WithArgs("u-1").
		WillReturnRows(pgxmock.NewRows([]string{"upload_id", "repository_id", "spool_path", "state", "offset_bytes", "digest", "started_at", "updated_at", "expires_at"}).
			AddRow("u-1", "repo-id", "/tmp/blob", "pending", int64(5), "", now, now, now.Add(time.Hour)))

	got, err := repo.GetUpload(context.Background(), "u-1")
	require.NoError(t, err)
	require.Equal(t, int64(5), got.OffsetBytes)

	mock.ExpectExec("UPDATE oci_blob_uploads").WithArgs("u-1", int64(10)).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	require.NoError(t, repo.UpdateUploadOffset(context.Background(), "u-1", 10))

	mock.ExpectExec("UPDATE oci_blob_uploads").WithArgs("u-1", string(domain.OCIBlobUploadStateFailed)).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	require.NoError(t, repo.MarkUploadFailed(context.Background(), "u-1"))

	mock.ExpectQuery("FROM oci_blob_uploads").WithArgs(now).
		WillReturnRows(pgxmock.NewRows([]string{"upload_id", "repository_id", "spool_path", "state", "offset_bytes", "digest", "started_at", "updated_at", "expires_at"}).
			AddRow("u-1", "repo-id", "/tmp/blob", "expired", int64(10), "", now, now, now.Add(-time.Minute)))

	expired, err := repo.ListExpiredUploads(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, expired, 1)
	require.Equal(t, domain.OCIBlobUploadState("expired"), expired[0].State)

	mock.ExpectExec("DELETE FROM oci_blob_uploads").WithArgs("u-1").WillReturnResult(pgxmock.NewResult("DELETE", 1))
	require.NoError(t, repo.DeleteUpload(context.Background(), "u-1"))

	require.NoError(t, mock.ExpectationsWereMet())
}
