# Verification report

Feature: `BAHIA_NOSTR_MIGRATION_BACKFILL_COMPLETION`

## Results

- `BNMBC-AC1`: passed in the focused migration suite.
- `BNMBC-AC2`: passed in the focused migration suite.
- `BNMBC-AC3`: passed in the focused migration suite.
- `BNMBC-AC4`: repository image build passed; canonical integration and the
  guarded production canary remain pending.

## Quality gates

- `go test ./internal/nostrmigration -count=1`: passed with the repository's
  pinned Go 1.26.3 toolchain.
- `go vet ./internal/nostrmigration`: passed with the same toolchain.
- `git diff --check`: passed.
- Repository Docker image build: passed as
  `sha256:d83efd31781fff61e1a3c201c7c469306c178aca1dfecbb817da7d8802d3887a`.

## Production-path assessment

The failed canary reached a healthy backend container but remained at
`bootstrap_ready: phase=init` because the ordered Tier-0 migration runner was
replaying legacy relay history before Bootstrapper could run. The live database
already has the exact historical terminal cursor (`9999-12-31T23:59:59Z`,
event id `~`). The compatibility path recognizes only that exact sentinel,
persists the new versioned completion marker, and skips replay. Ordinary
installations without either marker still run the full backfill and record
completion only after success.
