package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
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
		WillReturnRows(pgxmock.NewRows([]string{"id", "service_id", "environment_id", "deployment_unit_id", "artifact_id", "requested_by", "source_kind", "approval_status", "status", "supersedes_intent_id", "approval_metadata", "metadata", "desired_state", "desired_hash", "created_at", "approved_at", "updated_at"}).
			AddRow(id, svcID, envID, nil, artifactID, "npub1xyz", "auto_promote", "approved", "pending", nil, []byte(`{"reviewer":"ops"}`), []byte(`{"hive_ci_result_event_id":"result-evt-123"}`), nil, "", now, nil, now))

	intent, err := repo.GetByHiveResultEventID(context.Background(), "result-evt-123")
	require.NoError(t, err)
	require.NotNil(t, intent)
	require.Equal(t, id, intent.ID)
	require.Equal(t, "result-evt-123", intent.Metadata["hive_ci_result_event_id"])

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgDeploymentIntentRepository_ListApprovedWithoutRuns(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := &PgDeploymentIntentRepository{pool: mock}
	now := time.Now().UTC()
	serviceID := uuid.New()
	environmentID := uuid.New()
	artifactID := uuid.New()
	approvedID := uuid.New()
	autoApprovedID := uuid.New()

	expectedQuery := `
		SELECT ` + intentColumns + ` FROM deployment_intents di
		WHERE di.approval_status IN ('approved', 'not_required')
		  AND di.status = 'approved'
		  AND NOT EXISTS (
			SELECT 1 FROM deployment_runs dr WHERE dr.deployment_intent_id = di.id
		  )
		ORDER BY di.approved_at ASC NULLS LAST, di.created_at ASC
	`
	rows := pgxmock.NewRows([]string{"id", "service_id", "environment_id", "deployment_unit_id", "artifact_id", "requested_by", "source_kind", "approval_status", "status", "supersedes_intent_id", "approval_metadata", "metadata", "desired_state", "desired_hash", "created_at", "approved_at", "updated_at"}).
		AddRow(approvedID, serviceID, environmentID, nil, artifactID, "operator", "manual", "approved", "approved", nil, []byte(`{}`), []byte(`{}`), nil, "", now, nil, now).
		AddRow(autoApprovedID, serviceID, environmentID, nil, artifactID, "hive-ci-bridge", "auto_promote", "not_required", "approved", nil, []byte(`{}`), []byte(`{}`), nil, "", now, nil, now)
	mock.ExpectQuery(regexp.QuoteMeta(expectedQuery)).WillReturnRows(rows)

	intents, err := repo.ListApprovedWithoutRuns(context.Background())
	require.NoError(t, err)
	require.Len(t, intents, 2)
	require.Equal(t, approvedID, intents[0].ID)
	require.Equal(t, domain.ApprovalStatusApproved, intents[0].ApprovalStatus)
	require.Equal(t, autoApprovedID, intents[1].ID)
	require.Equal(t, domain.ApprovalStatusNotRequired, intents[1].ApprovalStatus)
	require.NoError(t, mock.ExpectationsWereMet())
}
