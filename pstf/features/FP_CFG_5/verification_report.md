# FP_CFG_5 verification

## Verified behavior

- Mounted YAML wins over the six legacy mutable-policy environment surfaces.
- Missing keys are seeded once and atomically persisted with file permissions retained.
- `bahia-server` and `bahia-relay` handle `SIGHUP` by validating the file and rebuilding their in-process runtime; the relay process can transition between enabled and disabled without container recreation.
- Docker Compose mounts `config.compose.yaml` at `/etc/bahia/config.yaml` for both services and no longer injects the affected policy variables.

## Quality gates

- `go test ./...` — passed.
- `go build ./...` — passed.
- `go vet ./...` — passed.
- `BAHIA_NOSTR_PRIVATE_KEY=test-only-placeholder docker compose config` — passed; the placeholder was used only for required-variable interpolation and was not written to the repository.
- `golangci-lint run ./cmd/server` and `./cmd/relay` — passed.
- Full/package config lint remains non-zero on pre-existing findings in `internal/config/config.go`; no finding points to `bootstrap_policy.go` after cleanup.
