# Stage 3 verification

Implemented the managed-instance supervisor, durable pending recovery claims, canonical subscription-driven Nostr projector, notification routing, supervision configuration, application wiring, and maintenance override service methods.

Verified on 2026-08-29:

- `go build ./...` — pass
- `go test ./internal/service/... ./internal/app/... ./internal/config/... ./internal/notifications/...` — pass
- `go test ./internal/repository` — pass
- `git diff --check` — pass
