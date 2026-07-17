package repository

import (
	"context"
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
