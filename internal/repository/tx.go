package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TxExecutor runs a logical repository write unit inside one transaction.
type TxExecutor interface {
	WithinTx(ctx context.Context, fn func(repos TxRepos) error) error
}

// TxRepos contains repositories bound to one transaction.
type TxRepos struct {
	Services     ServiceRepository
	Environments EnvironmentRepository
	Builds       BuildRepository
	Artifacts    ArtifactRepository
	State        EnvironmentServiceStateRepository
	Observations RuntimeObservationRepository
	Secrets      SecretRepository
}

type pgQueryer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// PgTxExecutor creates tx-scoped PostgreSQL repository bundles.
type PgTxExecutor struct {
	pool *pgxpool.Pool
}

// NewPgTxExecutor creates a PostgreSQL transaction executor.
func NewPgTxExecutor(pool *pgxpool.Pool) *PgTxExecutor {
	return &PgTxExecutor{pool: pool}
}

// WithinTx executes fn with repositories backed by a single database transaction.
func (e *PgTxExecutor) WithinTx(ctx context.Context, fn func(repos TxRepos) error) error {
	if e == nil || e.pool == nil {
		return fmt.Errorf("transaction executor is not configured")
	}
	tx, err := e.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	repos := TxRepos{
		Services:     newPgServiceRepositoryWithDB(tx),
		Environments: newPgEnvironmentRepositoryWithDB(tx),
		Builds:       newPgBuildRepositoryWithDB(tx),
		Artifacts:    newPgArtifactRepositoryWithDB(tx),
		State:        newPgEnvironmentServiceStateRepositoryWithDB(tx),
		Observations: newPgRuntimeObservationRepositoryWithDB(tx),
		Secrets:      newPgSecretRepositoryWithDB(tx),
	}
	if err := fn(repos); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	committed = true
	return nil
}
