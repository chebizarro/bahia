package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v5"
	"github.com/stretchr/testify/require"
)

func TestPgArtifactRepository_GetByImageRepoDigest(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := &PgArtifactRepository{pool: mock}
	now := time.Now().UTC()
	sizeBytes := int64(42)
	id := uuid.New()
	buildID := uuid.New()
	svcID := uuid.New()

	mock.ExpectQuery("FROM artifacts WHERE image_repo = \\$1 AND image_digest = \\$2").
		WithArgs("ghcr.io/acme/app", "sha256:abc").
		WillReturnRows(pgxmock.NewRows([]string{"id", "build_id", "service_id", "image_repo", "image_tag", "image_digest", "manifest_media_type", "size_bytes", "sbom_url", "signature_ref", "scan_status", "metadata", "created_at"}).
			AddRow(id, buildID, svcID, "ghcr.io/acme/app", "main", "sha256:abc", "application/vnd.oci.image.manifest.v1+json", &sizeBytes, "", "", "unknown", []byte(`{"source":"hive-ci"}`), now))

	artifact, err := repo.GetByImageRepoDigest(context.Background(), "ghcr.io/acme/app", "sha256:abc")
	require.NoError(t, err)
	require.NotNil(t, artifact)
	require.Equal(t, id, artifact.ID)
	require.Equal(t, "sha256:abc", artifact.ImageDigest)
	require.Equal(t, "hive-ci", artifact.Metadata["source"])

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgArtifactRepositorySharedScannerServesGetAndList(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now().UTC()
	id, buildID, serviceID := uuid.New(), uuid.New(), uuid.New()
	sizeBytes := int64(42)
	columns := []string{"id", "build_id", "service_id", "image_repo", "image_tag", "image_digest", "manifest_media_type", "size_bytes", "sbom_url", "signature_ref", "scan_status", "metadata", "created_at"}
	row := func() *pgxmock.Rows {
		return pgxmock.NewRows(columns).AddRow(id, buildID, serviceID, "ghcr.io/acme/app", "main", "sha256:abc", nil, &sizeBytes, nil, nil, nil, []byte(`{"source":"hive-ci"}`), now)
	}

	mock.ExpectQuery("FROM artifacts WHERE id = \\$1").WithArgs(id).WillReturnRows(row())
	mock.ExpectQuery("FROM artifacts WHERE build_id = \\$1").WithArgs(buildID).WillReturnRows(row())
	repo := newPgArtifactRepositoryWithDB(mock)

	artifact, err := repo.GetByID(context.Background(), id)
	require.NoError(t, err)
	artifacts, err := repo.ListByBuild(context.Background(), buildID)
	require.NoError(t, err)
	require.Len(t, artifacts, 1)
	require.Equal(t, *artifact, artifacts[0])
	require.Equal(t, "unknown", string(artifacts[0].ScanStatus))
	require.NoError(t, mock.ExpectationsWereMet())
}
