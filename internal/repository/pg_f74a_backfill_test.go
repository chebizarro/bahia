package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v5"
	"github.com/stretchr/testify/require"
)

func TestF74aSourceRejectsUnboundedPages(t *testing.T) {
	r := &PgF74aBackfillSource{}
	_, err := r.ListSemanticPackagesAfter(context.Background(), uuid.Nil, F74aPageLimit+1)
	require.ErrorContains(t, err, "page limit")
	_, err = r.ListLegacyPackagesAfter(context.Background(), uuid.Nil, 0)
	require.ErrorContains(t, err, "page limit")
}

func TestF74aSemanticPackagePageUsesLowestIDAndNullNormalizedKey(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	cursor, sbomID, representative := uuid.New(), uuid.New(), uuid.New()
	mock.ExpectQuery("FROM sbom_packages p").WithArgs(cursor, 2).
		WillReturnRows(pgxmock.NewRows([]string{"id", "sbom_id", "name", "version", "ecosystem", "license", "purl", "cpe"}).
			AddRow(representative, sbomID, "pkg", "1", nil, nil, nil, nil))
	items, err := newPgF74aBackfillSourceWithDB(mock).ListSemanticPackagesAfter(context.Background(), cursor, 2)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, representative, items[0].ID)
	require.Equal(t, "", items[0].Ecosystem)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestF74aLinkedObservationRejectsCrossCoordinateLink(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	stateService, obsService, envID := uuid.New(), uuid.New(), uuid.New()
	mock.ExpectQuery("FROM environment_service_state s JOIN runtime_observations o").
		WithArgs(uuid.Nil, uuid.Nil, 1).
		WillReturnRows(pgxmock.NewRows([]string{"state_service", "state_environment", "id", "service_id", "environment_id", "unit", "digest", "repo", "container", "host", "version", "health", "source", "metadata", "normalized", "hash", "at"}).
			AddRow(stateService, envID, uuid.New(), obsService, envID, nil, "sha256:abc", "", "", "", "", "healthy", "runtime", []byte(`{}`), nil, "", time.Now()))
	_, err = newPgF74aBackfillSourceWithDB(mock).ListLinkedObservationsAfter(context.Background(), F74aStateCursor{}, 1)
	require.ErrorContains(t, err, "invalid coordinate")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestF74aObservationRunScanPreservesFirstMaterialAndLinkedRows(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	serviceID, envID := uuid.New(), uuid.New()
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	rows := pgxmock.NewRows([]string{"id", "service", "env", "unit", "digest", "repo", "container", "host", "version", "health", "source", "metadata", "normalized", "hash", "at", "linked"})
	for i := range ids {
		digest := "sha256:" + strings.Repeat("a", 64)
		if i == 3 {
			digest = "sha256:" + strings.Repeat("b", 64)
		}
		rows.AddRow(ids[i], serviceID, envID, nil, digest, "repo", "container", "host", "v1", "healthy", "runtime", []byte(`{}`), nil, "", base.Add(time.Duration(i)*time.Hour), i == 2)
	}
	mock.ExpectQuery("SELECT o.id").WillReturnRows(rows)
	var candidates []uuid.UUID
	total, linked, material, suppressible, err := scanF74aRuns(context.Background(), mock, base.Add(24*time.Hour), func(id uuid.UUID) error {
		candidates = append(candidates, id)
		return nil
	})
	require.NoError(t, err)
	require.EqualValues(t, 4, total)
	require.EqualValues(t, 1, linked)
	require.EqualValues(t, 2, material)
	require.EqualValues(t, 1, suppressible)
	require.Equal(t, []uuid.UUID{ids[1]}, candidates)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestF74aCompactionGuardRejectsMissingBackupAndUnboundedBatch(t *testing.T) {
	_, err := CompactF74aObservations(context.Background(), nil, time.Now().Add(-time.Hour), 1, " ", nil)
	require.ErrorContains(t, err, "backup reference")
	_, err = CompactF74aObservations(context.Background(), nil, time.Now().Add(-time.Hour), F74aPageLimit+1, "backup-verified", nil)
	require.ErrorContains(t, err, "batch size")
}
