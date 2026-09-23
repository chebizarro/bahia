package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v5"
	"github.com/stretchr/testify/require"
)

func TestPgBudgetPolicyRepositorySharedScannerServesGetAndList(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	id := uuid.New()
	now := time.Now().UTC()
	columns := []string{"id", "version", "name", "agent_pubkey", "task_id", "scope", "limits", "enabled", "created_at", "updated_at"}
	row := func() *pgxmock.Rows {
		return pgxmock.NewRows(columns).AddRow(id, int64(2), "agent budget", "pubkey", "", "agent", []byte(`[{"resource_type":"tokens","max_amount":100}]`), true, now, now)
	}

	mock.ExpectQuery("FROM budget_policies WHERE id = \\$1").WithArgs(id).WillReturnRows(row())
	mock.ExpectQuery("FROM budget_policies ORDER BY name, version DESC").WillReturnRows(row())
	repo := &PgBudgetPolicyRepository{pool: mock}

	policy, err := repo.GetByID(context.Background(), id)
	require.NoError(t, err)
	policies, err := repo.List(context.Background(), false)
	require.NoError(t, err)
	require.Len(t, policies, 1)
	require.Equal(t, *policy, policies[0])
	require.Len(t, policies[0].Limits, 1)
	require.NoError(t, mock.ExpectationsWereMet())
}
