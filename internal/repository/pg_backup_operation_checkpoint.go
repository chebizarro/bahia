package repository

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (r *PgBackupControlPlaneRepository) StartBackupOperation(ctx context.Context, operationType string, operationID, token uuid.UUID) (*BackupOperationCheckpoint, bool, error) {
	checkpoint := &BackupOperationCheckpoint{}
	var executedAt pgtype.Timestamptz
	err := r.pool.QueryRow(ctx, `
		INSERT INTO backup_operation_checkpoints (operation_type, operation_id, token, status)
		VALUES ($1, $2, $3, 'executing')
		ON CONFLICT (operation_type, operation_id) DO NOTHING
		RETURNING operation_type, operation_id, token, status, result, created_at, executed_at
	`, operationType, operationID, token).Scan(
		&checkpoint.OperationType, &checkpoint.OperationID, &checkpoint.Token, &checkpoint.Status,
		&checkpoint.Result, &checkpoint.CreatedAt, &executedAt,
	)
	if err == nil {
		checkpoint.ExecutedAt = timePtrFromPG(executedAt)
		return checkpoint, true, nil
	}
	if err != pgx.ErrNoRows {
		return nil, false, fmt.Errorf("starting backup operation checkpoint: %w", err)
	}
	err = r.pool.QueryRow(ctx, `
		SELECT operation_type, operation_id, token, status, result, created_at, executed_at
		FROM backup_operation_checkpoints
		WHERE operation_type = $1 AND operation_id = $2
	`, operationType, operationID).Scan(
		&checkpoint.OperationType, &checkpoint.OperationID, &checkpoint.Token, &checkpoint.Status,
		&checkpoint.Result, &checkpoint.CreatedAt, &executedAt,
	)
	if err != nil {
		return nil, false, fmt.Errorf("loading existing backup operation checkpoint: %w", err)
	}
	checkpoint.ExecutedAt = timePtrFromPG(executedAt)
	return checkpoint, false, nil
}

func (r *PgBackupControlPlaneRepository) MarkBackupOperationExecuted(ctx context.Context, operationType string, operationID, token uuid.UUID, result json.RawMessage) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE backup_operation_checkpoints
		SET status = 'executed', result = $4, executed_at = NOW()
		WHERE operation_type = $1 AND operation_id = $2 AND token = $3 AND status = 'executing'
	`, operationType, operationID, token, result)
	if err != nil {
		return fmt.Errorf("marking backup operation executed: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("backup operation checkpoint changed before execution result was persisted: %w", ErrConflict)
	}
	return nil
}
