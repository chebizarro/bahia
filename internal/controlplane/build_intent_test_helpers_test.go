package controlplane

import (
	"context"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

type buildResultTestLoader struct{ build *domain.Build }

func (f buildResultTestLoader) GetByID(context.Context, uuid.UUID) (*domain.Build, error) {
	return f.build, nil
}
