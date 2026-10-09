package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/pashagolub/pgxmock/v5"
	"github.com/stretchr/testify/require"
)

func TestArtifactPackageKeysPreventRepeatedCompatibilityRows(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	sbomID := uuid.New()
	mock.ExpectQuery("SELECT name, version, COALESCE").
		WithArgs(sbomID).
		WillReturnRows(pgxmock.NewRows([]string{"name", "version", "ecosystem", "license", "purl", "cpe"}).
			AddRow("existing", "1", "", "MIT", "", ""))

	keys, err := artifactPackageKeys(context.Background(), mock, sbomID)
	require.NoError(t, err)
	incoming := []domain.SBOMManifestPackage{
		{Name: "existing", Version: "1", License: "MIT"},
		{Name: "new", Version: "2", Ecosystem: "npm", PURL: "pkg:npm/new@2"},
		{Name: "new", Version: "2", Ecosystem: "npm", PURL: "pkg:npm/new@2"},
	}
	created := newArtifactPackages(sbomID, incoming, keys)
	require.Len(t, created, 1)
	require.Equal(t, "new", created[0].Name)
	require.Equal(t, sbomID, created[0].SBOMID)
	require.Empty(t, newArtifactPackages(sbomID, incoming, keys))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectManifestRejectsArtifactWithoutPayloadHash(t *testing.T) {
	repo := &PgSBOMRepository{}
	manifest := &domain.SBOMManifest{Subject: domain.SBOMSubject{Type: domain.SBOMSubjectArtifact}}
	err := repo.ProjectManifest(context.Background(), manifest, nil)
	require.ErrorContains(t, err, "payload SHA-256 is required")
}

func TestCreatePackagesReusesHistoricalRepresentativeID(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	sbomID, prior := uuid.New(), uuid.New()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id FROM artifact_sboms WHERE id = ").WithArgs(sbomID).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(sbomID))
	mock.ExpectQuery("SELECT id, name, version, COALESCE").WithArgs(sbomID).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "version", "ecosystem", "license", "purl", "cpe"}).
			AddRow(prior, "existing", "1", "", "", "", ""))
	mock.ExpectCommit()
	packages := []domain.SBOMPackage{{SBOMID: sbomID, Name: "existing", Version: "1"}}
	repo := &PgSBOMRepository{pool: mock}
	require.NoError(t, repo.CreatePackages(context.Background(), packages))
	require.Equal(t, prior, packages[0].ID)
	require.NoError(t, mock.ExpectationsWereMet())
}
