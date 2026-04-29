package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
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
