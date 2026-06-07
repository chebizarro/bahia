# Verification Report: bahia-5lzn

Date: 2026-06-07

## Intended behavior

`PgSecretRepository.DeleteByName` must delete service-wide secrets when `envID` is nil using SQL placeholders that match the two arguments passed to pgx: `serviceID` and `name`.

## Observed defect

Before this pass, the nil-`envID` branch used `name = $3` while passing only `serviceID` and `name`, which would fail at runtime for service-wide secret deletion by name.

## Fix evidence

Changed `internal/repository/pg_secret.go` so the nil-`envID` branch uses `name = $2`.

Added `TestPgSecretRepositoryDeleteByNameServiceWideUsesNamePlaceholder` in `internal/repository/pg_secret_test.go` to assert the service-wide delete path executes SQL with `$1`/`$2` and binds exactly `(serviceID, name)`.

## Verification

- `go test ./internal/repository -run 'TestPgSecretRepository(DeleteByNameServiceWideUsesNamePlaceholder|CreateWritesVersionRow|UpdateWritesNextVersionRow|RecordSecretAccessAudit)'` — passed.

## Status

AC1 is verified. The bead is closable after orchestrator Beads/admin closeout.
