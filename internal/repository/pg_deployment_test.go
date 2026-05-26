package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPgDeploymentIntentRepository_GetByHiveResultEventID(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := &PgDeploymentIntentRepository{pool: mock}
	now := time.Now().UTC()
	id := uuid.New()
	svcID := uuid.New()
	envID := uuid.New()
	artifactID := uuid.New()

	mock.ExpectQuery("metadata->>'hive_ci_result_event_id' = \\$1").
		WithArgs("result-evt-123").
		WillReturnRows(pgxmock.NewRows([]string{"id", "service_id", "environment_id", "artifact_id", "requested_by", "source_kind", "approval_status", "status", "supersedes_intent_id", "approval_metadata", "metadata", "desired_state", "desired_hash", "created_at", "approved_at", "updated_at"}).
			AddRow(id, svcID, envID, artifactID, "npub1xyz", "auto_promote", "approved", "pending", nil, []byte(`{"reviewer":"ops"}`), []byte(`{"hive_ci_result_event_id":"result-evt-123"}`), nil, "", now, nil, now))

	intent, err := repo.GetByHiveResultEventID(context.Background(), "result-evt-123")
	require.NoError(t, err)
	require.NotNil(t, intent)
	require.Equal(t, id, intent.ID)
	require.Equal(t, "result-evt-123", intent.Metadata["hive_ci_result_event_id"])

	require.NoError(t, mock.ExpectationsWereMet())
}
