package repository

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestPgPaymentRepositoryCreateRejectsUnserializableMetadataWithoutMutation(t *testing.T) {
	repo := &PgPaymentRepository{}
	payment := &domain.PaymentRecord{
		Metadata: map[string]any{"invalid": make(chan struct{})},
	}

	err := repo.Create(context.Background(), payment)
	require.ErrorContains(t, err, "marshaling payment metadata")
	require.Equal(t, uuid.Nil, payment.ID)
	require.True(t, payment.CreatedAt.IsZero())
	require.True(t, payment.UpdatedAt.IsZero())
}

func TestPgPaymentRepositoryUsesMigratedPaymentRecordsTable(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	source, err := os.ReadFile(filepath.Join(filepath.Dir(file), "pg_payment.go"))
	require.NoError(t, err)

	text := string(source)
	require.NotContains(t, text, "FROM payments")
	require.NotContains(t, text, "INTO payments")
	require.NotContains(t, text, "UPDATE payments")
	require.GreaterOrEqual(t, strings.Count(text, "payment_records"), 7)
}
