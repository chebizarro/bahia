package repository

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// uniqueViolationConstraint returns the violated constraint name when err is a
// Postgres unique_violation (23505), and "" otherwise.
func uniqueViolationConstraint(err error) string {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return ""
	}
	if pgErr.ConstraintName == "" {
		return "unknown"
	}
	return pgErr.ConstraintName
}
