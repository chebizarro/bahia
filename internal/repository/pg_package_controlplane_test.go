package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPgPackageControlPlaneRepository_UpsertRepositoryUsesStaleEventGuard(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgPackageControlPlaneRepositoryWithDB(mock)
	projection := &domain.PackageRepository{
		ID:                     uuid.New(),
		Name:                   "frontend-npm",
		Format:                 domain.PackageRepositoryFormatNPM,
		BackendRef:             "nexus-prod",
		BackendType:            domain.PackageBackendNexus,
		ExternalRepositoryName: "raw-npm",
		Policy: domain.PackageRepositoryPolicy{
			RequireSHA256: true,
		},
		Metadata:           map[string]any{"owner": "platform"},
		Status:             domain.PackageRepositoryStatusReady,
		LastEventID:        "evt-2",
		LastEventCreatedAt: time.Unix(200, 0).UTC(),
	}

	mock.ExpectExec("INSERT INTO package_repositories_projection").
		WithArgs(projection.ID, projection.Name, projection.Format, projection.BackendRef, projection.BackendType,
			projection.ExternalRepositoryName, projection.Description, projection.NamespacePrefix, pgxmock.AnyArg(),
			pgxmock.AnyArg(), projection.PublicURL, projection.Status, projection.LastError, projection.Deleted,
			pgxmock.AnyArg(), pgxmock.AnyArg(), projection.LastEventID, projection.LastEventCreatedAt).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	require.NoError(t, repo.UpsertRepository(ctx, projection))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgPackageControlPlaneRepository_GetRepositoryScansJSONAndEnums(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgPackageControlPlaneRepositoryWithDB(mock)
	id := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT " + packageRepositoryColumns + " FROM package_repositories_projection WHERE id = $1")).
		WithArgs(id).
		WillReturnRows(pgxmock.NewRows(splitColumns(packageRepositoryColumns)).
			AddRow(id, "python", "pypi", "pulp", "pulp", "python", "desc", "py/", []byte(`{"require_sha256":true,"allow_overwrite":false}`), []byte(`{"team":"ml"}`), "https://pkg.example.com/python", "ready", "", false, now, now, "evt-1", now))

	got, err := repo.GetRepository(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, domain.PackageRepositoryFormatPyPI, got.Format)
	require.Equal(t, domain.PackageBackendPulp, got.BackendType)
	require.True(t, got.Policy.RequireSHA256)
	require.Equal(t, "ml", got.Metadata["team"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgPackageControlPlaneRepository_UpsertArtifactUsesCompositeIdentityAndStaleGuard(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgPackageControlPlaneRepositoryWithDB(mock)
	artifact := &domain.PackageArtifact{
		ID:                 uuid.New(),
		RepositoryID:       uuid.New(),
		RepositoryName:     "frontend-npm",
		Format:             domain.PackageRepositoryFormatNPM,
		PackageName:        "@acme/web",
		Version:            "1.2.3",
		Filename:           "acme-web-1.2.3.tgz",
		SHA256:             "abc",
		Status:             domain.PackageArtifactStatusAvailable,
		LastEventID:        "evt-artifact",
		LastEventCreatedAt: time.Unix(300, 0).UTC(),
	}

	mock.ExpectExec("INSERT INTO package_artifacts_projection").
		WithArgs(artifact.ID, artifact.RepositoryID, artifact.RepositoryName, artifact.Format, artifact.Namespace,
			artifact.PackageName, artifact.Version, artifact.Filename, artifact.SourceURL, artifact.SHA256,
			artifact.SizeBytes, artifact.ContentType, pgxmock.AnyArg(), artifact.DownloadURL, artifact.BackendPath,
			artifact.Status, artifact.LastError, artifact.Deleted, pgxmock.AnyArg(), pgxmock.AnyArg(),
			artifact.LastEventID, artifact.LastEventCreatedAt).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	require.NoError(t, repo.UpsertArtifact(ctx, artifact))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgPackageControlPlaneRepository_UpsertPublicationStoresPromotionState(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgPackageControlPlaneRepositoryWithDB(mock)
	targetRepoID := uuid.New()
	approvedAt := time.Unix(400, 0).UTC()
	publication := &domain.PackagePublication{
		ID:                 uuid.New(),
		RepositoryID:       uuid.New(),
		ArtifactID:         uuid.New(),
		Environment:        "prod",
		Channel:            "stable",
		TargetRepositoryID: &targetRepoID,
		Status:             domain.PackagePublicationStatusPromoted,
		PolicyDecision:     domain.PackagePolicyDecisionAllowed,
		PolicyRef:          "policy/package-prod",
		ApprovedBy:         "operator-pubkey",
		ApprovedAt:         &approvedAt,
		Metadata:           map[string]any{"ticket": "REL-123"},
		LastEventID:        "evt-publication",
		LastEventCreatedAt: time.Unix(401, 0).UTC(),
	}

	mock.ExpectExec("INSERT INTO package_publications_projection").
		WithArgs(publication.ID, publication.RepositoryID, publication.ArtifactID, publication.Environment,
			publication.Channel, targetRepoID, publication.Status, publication.PolicyDecision, publication.PolicyRef,
			publication.ApprovedBy, publication.ApprovedAt, publication.PublishedAt, publication.PromotedAt,
			publication.LastError, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), publication.LastEventID,
			publication.LastEventCreatedAt).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	require.NoError(t, repo.UpsertPublication(ctx, publication))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgPackageControlPlaneRepository_UpsertIntentKeepsRequestEventIDIdempotent(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgPackageControlPlaneRepositoryWithDB(mock)
	repositoryID := uuid.New()
	intent := &domain.PackageIntent{
		ID:              uuid.New(),
		RequestEventID:  "nostr-request-1",
		Operation:       domain.PackageOperationArtifactPublish,
		RepositoryID:    &repositoryID,
		RepositoryName:  "frontend-npm",
		PackageName:     "@acme/web",
		Version:         "1.2.3",
		Filename:        "acme-web-1.2.3.tgz",
		RequesterPubkey: "pubkey",
		RequestPayload:  map[string]any{"source_url": "https://blob.example/pkg.tgz"},
		Status:          domain.PackageIntentStatusExecuting,
	}

	mock.ExpectExec("INSERT INTO package_intents_projection").
		WithArgs(intent.ID, intent.RequestEventID, intent.Operation, repositoryID, intent.RepositoryName,
			nil, intent.Namespace, intent.PackageName, intent.Version, intent.Filename, intent.RequesterPubkey,
			pgxmock.AnyArg(), pgxmock.AnyArg(), intent.Status, intent.ErrorMessage, pgxmock.AnyArg(),
			pgxmock.AnyArg(), intent.CompletedAt, intent.LastStatusEventID, intent.LastResultEventID).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	require.NoError(t, repo.UpsertIntent(ctx, intent))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgPackageControlPlaneRepository_ListNonTerminalIntents(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgPackageControlPlaneRepositoryWithDB(mock)
	id := uuid.New()
	repositoryID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	mock.ExpectQuery("FROM package_intents_projection").
		WithArgs(25).
		WillReturnRows(pgxmock.NewRows(splitColumns(packageIntentColumns)).
			AddRow(id, "req-1", "repository_apply", repositoryID.String(), "frontend-npm", nil, "", "", "", "", "pubkey", []byte(`{"name":"frontend-npm"}`), []byte(`{}`), "accepted", "", now, now, nil, "status-1", ""))

	intents, err := repo.ListNonTerminalIntents(ctx, 25)
	require.NoError(t, err)
	require.Len(t, intents, 1)
	require.Equal(t, domain.PackageOperationRepositoryApply, intents[0].Operation)
	require.NotNil(t, intents[0].RepositoryID)
	require.Equal(t, repositoryID, *intents[0].RepositoryID)
	require.Equal(t, "frontend-npm", intents[0].RequestPayload["name"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func splitColumns(columns string) []string {
	parts := regexp.MustCompile(`,\s*`).Split(columns, -1)
	for i, part := range parts {
		parts[i] = regexp.MustCompile(`\s+`).ReplaceAllString(part, "")
	}
	return parts
}
