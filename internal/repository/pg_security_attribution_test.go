package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/pashagolub/pgxmock/v5"
	"github.com/stretchr/testify/require"
)

func TestSecurityAttributionIndexOnlyAcceptsCanonicalSource(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	t.Cleanup(func() { mock.Close() })
	index := &PgSecurityAttributionIndex{pool: mock}
	edge := domain.SecurityAttribution{OrgID: uuid.New(), SubjectType: "sbom", SubjectDigest: strings.Repeat("a", 64), Visible: true}
	require.ErrorContains(t, index.UpsertAttribution(t.Context(), edge, "", 1), "canonical event id")
	mock.ExpectExec("DELETE FROM security_attributions").WillReturnResult(pgxmock.NewResult("DELETE", 1))
	require.NoError(t, index.ResetAttributionIndex(t.Context()))
	mock.ExpectExec("INSERT INTO security_attributions").
		WithArgs(edge.OrgID, edge.SubjectType, edge.SubjectDigest, true, strings.Repeat("b", 64), int64(100)).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	require.NoError(t, index.UpsertAttribution(context.Background(), edge, strings.Repeat("b", 64), 100))
	require.NoError(t, mock.ExpectationsWereMet())
}
