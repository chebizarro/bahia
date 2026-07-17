package repository

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

const (
	BackupOperationSnapshot = "snapshot"
	BackupOperationRestore  = "restore"

	BackupCheckpointExecuting = "executing"
	BackupCheckpointExecuted  = "executed"
)

// BackupOperationCheckpoint durably fences a non-idempotent backend operation.
type BackupOperationCheckpoint struct {
	OperationType string
	OperationID   uuid.UUID
	Token         uuid.UUID
	Status        string
	Result        json.RawMessage
	CreatedAt     time.Time
	ExecutedAt    *time.Time
}

type BackupOperationCheckpointRepository interface {
	StartBackupOperation(ctx context.Context, operationType string, operationID, token uuid.UUID) (*BackupOperationCheckpoint, bool, error)
	MarkBackupOperationExecuted(ctx context.Context, operationType string, operationID, token uuid.UUID, result json.RawMessage) error
}
