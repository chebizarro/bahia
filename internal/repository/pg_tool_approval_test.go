package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/pashagolub/pgxmock/v5"
)

func TestPgToolProvisioningRepositoryApplyToolApprovalDecisionUsesPendingCAS(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	repo := newPgToolProvisioningRepositoryWithDB(mock)
	id, serviceID, environmentID := uuid.New(), uuid.New(), uuid.New()
	now := time.Now().UTC()
	columns := strings.Split(strings.ReplaceAll(toolIntentColumns, " ", ""), ",")
	mock.ExpectQuery(`UPDATE tool_provision_intents.*WHERE id = \$1 AND status = \$5.*RETURNING`).
		WithArgs(id, domain.ToolProvisionStatusApproved, "operator", now, domain.ToolProvisionStatusAwaitingApproval).
		WillReturnRows(pgxmock.NewRows(columns).AddRow(id, serviceID, environmentID, []byte(`[]`), []byte(`[]`), nil, "", domain.ToolProvisionStatusApproved, true, []byte(`[]`), "operator", nil, "", "", now))

	intent, err := repo.ApplyToolApprovalDecision(context.Background(), id, domain.ToolProvisionStatusApproved, "operator", now)
	if err != nil {
		t.Fatalf("apply approval decision: %v", err)
	}
	if intent.Status != domain.ToolProvisionStatusApproved || intent.ApprovedBy != "operator" {
		t.Fatalf("unexpected decided intent: %+v", intent)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPgToolProvisioningRepositoryApplyToolApprovalDecisionRejectsNonPendingIntent(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	repo := newPgToolProvisioningRepositoryWithDB(mock)
	id := uuid.New()
	now := time.Now().UTC()
	columns := strings.Split(strings.ReplaceAll(toolIntentColumns, " ", ""), ",")
	mock.ExpectQuery(`UPDATE tool_provision_intents.*WHERE id = \$1 AND status = \$5.*RETURNING`).
		WithArgs(id, domain.ToolProvisionStatusRejected, nil, nil, domain.ToolProvisionStatusAwaitingApproval).
		WillReturnRows(pgxmock.NewRows(columns))

	intent, err := repo.ApplyToolApprovalDecision(context.Background(), id, domain.ToolProvisionStatusRejected, "operator", now)
	if intent != nil || !errors.Is(err, ErrConflict) {
		t.Fatalf("intent=%+v error=%v, want ErrConflict", intent, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
