package repository

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPgSBOMRepository_CreateManifest_DedupesBySubjectFormatGeneratorPayload(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := &PgSBOMRepository{pool: mock}
	storedID := uuid.New()
	createdAt := time.Now().UTC().Add(-time.Minute)
	updatedAt := time.Now().UTC()
	manifest := &domain.SBOMManifest{
		ID: uuid.New(),
		Subject: domain.SBOMSubject{
			Type:        domain.SBOMSubjectRepository,
			ID:          "github.com/openagentsinc/bahia",
			DisplayName: "bahia",
			Digest:      "git:abc123",
		},
		Format:        domain.SBOMFormatSPDX,
		MediaType:     "application/spdx+json",
		StorageType:   domain.SBOMStorageBlossom,
		StorageURI:    "https://blossom.example/sbom",
		PayloadSHA256: "sha256:payload",
		Generator:     domain.SBOMGenerator{ID: "syft", Version: "1.0.0", Pubkey: "pubkey"},
		PackageCount:  2,
		NTIAStatus:    "compliant",
		NTIA:          &domain.NTIACompliance{IsCompliant: true},
		PublishState:  domain.SBOMPublishDraft,
		SourceKind:    domain.SBOMSourceGenerated,
		Metadata:      map[string]any{"source": "test"},
	}

	mock.ExpectQuery("INSERT INTO sbom_manifests").
		WithArgs(anyArgs(30)...).
		WillReturnRows(pgxmock.NewRows(splitColumns(sbomManifestColumns)).
			AddRow(storedID, "repository", manifest.Subject.ID, strPtr(manifest.Subject.DisplayName), manifest.Subject.Digest,
				"spdx", strPtr(manifest.MediaType), "blossom", manifest.StorageURI, manifest.PayloadSHA256,
				"syft", strPtr("1.0.0"), strPtr("pubkey"), 2, 0, 0, 0, strPtr("compliant"), []byte(`{"isCompliant":true}`),
				nil, nil, nil, nil, "draft", nil, "generated", []byte(`{"source":"test"}`), createdAt, updatedAt, nil))

	err = repo.CreateManifest(context.Background(), manifest)
	require.NoError(t, err)
	require.Equal(t, storedID, manifest.ID)
	require.Equal(t, domain.SBOMSubjectRepository, manifest.Subject.Type)
	require.Equal(t, domain.SBOMPublishDraft, manifest.PublishState)
	require.Equal(t, "test", manifest.Metadata["source"])
	require.NotNil(t, manifest.NTIA)
	require.True(t, manifest.NTIA.IsCompliant)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgSBOMRepository_ListManifestsBySubject(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := &PgSBOMRepository{pool: mock}
	subject := domain.SBOMSubject{Type: domain.SBOMSubjectPackage, ID: "pkg:npm/lodash@4.17.21", Digest: "sha256:pkg"}
	now := time.Now().UTC()
	id := uuid.New()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT "+sbomManifestColumns+" FROM sbom_manifests")).
		WithArgs("package", subject.ID, subject.Digest, 25).
		WillReturnRows(pgxmock.NewRows(splitColumns(sbomManifestColumns)).
			AddRow(id, "package", subject.ID, nil, subject.Digest,
				"cyclonedx", strPtr("application/vnd.cyclonedx+json"), "blossom", "https://blossom.example/pkg", "sha256:payload",
				"syft", nil, nil, 1, 0, 0, 0, strPtr("unknown"), []byte(`null`),
				strPtr("ref-event"), strPtr("sbom:ref:key"), strPtr("list-event"), strPtr("sbom:available:package:key"), "published", nil, "imported", []byte(`{}`), now, now, &now))

	manifests, err := repo.ListManifestsBySubject(context.Background(), subject, 25)
	require.NoError(t, err)
	require.Len(t, manifests, 1)
	require.Equal(t, id, manifests[0].ID)
	require.Equal(t, domain.SBOMPublishPublished, manifests[0].PublishState)
	require.Equal(t, "ref-event", manifests[0].ReferenceEventID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgSBOMRepository_UpdateManifestPublishState(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := &PgSBOMRepository{pool: mock}
	id := uuid.New()
	mock.ExpectExec("UPDATE sbom_manifests").
		WithArgs(id, "published", "ref-event", "list-event", nil, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = repo.UpdateManifestPublishState(context.Background(), id, domain.SBOMPublishPublished, "ref-event", "list-event", "")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgSBOMRepository_UpdateCompatibilityVulnerabilityCounts(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := &PgSBOMRepository{pool: mock}
	artifactID := uuid.New()
	payload := strings.Repeat("a", 64)
	counts := domain.SecuritySeverityCounts{Critical: 1, High: 2, Moderate: 3}
	mock.ExpectExec("UPDATE artifact_sboms").
		WithArgs(artifactID, payload, 6, 1, 2).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE sbom_manifests").
		WithArgs(artifactID.String(), payload, 6, 1, 2, pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	require.NoError(t, repo.UpdateCompatibilityVulnerabilityCounts(context.Background(), artifactID, payload, counts, 6))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestArtifactSBOMFromManifest_PreservesArtifactCompatibilityProjection(t *testing.T) {
	artifactID := uuid.New()
	manifestID := uuid.New()
	manifest := &domain.SBOMManifest{
		ID:                 manifestID,
		Subject:            domain.SBOMSubject{Type: domain.SBOMSubjectArtifact, ID: artifactID.String(), Digest: "sha256:artifact"},
		Format:             domain.SBOMFormatCycloneDX,
		StorageURI:         "https://blossom.example/artifact-sbom",
		PayloadSHA256:      "sha256:payload",
		PackageCount:       3,
		VulnerabilityCount: 2,
		CriticalCount:      1,
		HighCount:          1,
		SourceKind:         domain.SBOMSourceImported,
	}

	artifactSBOM := artifactSBOMFromManifest(manifest, artifactID)
	require.Equal(t, artifactID, artifactSBOM.ArtifactID)
	require.Equal(t, domain.SBOMFormatCycloneDX, artifactSBOM.Format)
	require.Equal(t, manifest.StorageURI, artifactSBOM.SourceURL)
	require.Equal(t, manifest.PayloadSHA256, artifactSBOM.RawHash)
	require.Equal(t, manifestID.String(), artifactSBOM.Metadata["sbom_manifest_id"])
	require.Equal(t, "sha256:artifact", artifactSBOM.Metadata["subject_digest"])
}

func TestPgSBOMRepository_SearchManifestPackagesByName(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := &PgSBOMRepository{pool: mock}
	manifestID := uuid.New()
	pkgID := uuid.New()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT "+manifestPkgColumns+" FROM sbom_manifest_packages WHERE name ILIKE $1 ORDER BY name LIMIT $2")).
		WithArgs("%lodash%", 10).
		WillReturnRows(pgxmock.NewRows(splitColumns(manifestPkgColumns)).
			AddRow(pkgID, manifestID, "lodash", "4.17.21", strPtr("npm"), strPtr("MIT"), strPtr("pkg:npm/lodash@4.17.21"), nil))

	packages, err := repo.SearchManifestPackagesByName(context.Background(), "lodash", 10)
	require.NoError(t, err)
	require.Len(t, packages, 1)
	require.Equal(t, pkgID, packages[0].ID)
	require.Equal(t, manifestID, packages[0].ManifestID)
	require.Equal(t, "pkg:npm/lodash@4.17.21", packages[0].PURL)
	require.NoError(t, mock.ExpectationsWereMet())
}

func strPtr(value string) *string {
	return &value
}

func anyArgs(count int) []any {
	args := make([]any, count)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}
