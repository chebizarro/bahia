# Bahia relay endpoint reconnect verification

## Implemented

- The shared relay pool checks `nostr.Relay.IsConnected()` instead of trusting
  only Bahia's cached `connected` flag.
- A stale relay object is closed and replaced before subscription filters are
  reissued.
- Health remains disconnected after a failed replacement connection.
- Recovery remains event-driven; no delivery polling or completion timer was
  introduced.

## Verification

- Focused deterministic adapter tests pass in the repository's pinned Go
  builder container.
- `go test ./internal/adapters/nostr -count=1` passes.
- `CGO_ENABLED=1 go test -race ./internal/adapters/nostr -count=1` passes in
  the pinned builder with an ephemeral Alpine compiler toolchain.
- `go test ./... -count=1` and `go vet ./...` pass as an unprivileged user in
  the pinned builder with the release-contract test prerequisites (`bash` and
  `python3`) installed.
- PSTF JSON validation and `git diff --check` pass.
- The repository Dockerfile builds successfully from commit
  `d98850f9c56f86bf41a2c58dea783eb2442c213e` with OCI image ID
  `sha256:5ad6db76023057b61b88c36c54e5963eb5dde762ef2a479c9630ff13c1fd5eb8`.
  Its OCI revision and version labels identify that exact source commit and
  version `0.1.0-d98850f9`.

## Remaining acceptance

Deploy the repository-built artifact through Bahia's supported signer-first
control plane, interrupt and restore a disposable relay endpoint, and prove the
same Bahia process reconnects and resumes scoped DNS/control-plane subscriptions.
