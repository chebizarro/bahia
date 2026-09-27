# Bahia global outbound Nostr admission verification

## Implemented

- `internal/nostrout` is the single process-wide admission controller
  (`Default()`), shared by `RelayPool`, the SoulFactory relay bus, Signet
  management gift wraps, and every NIP-46 bunker (`nostrout.Bunker`).
- Aggregate and per-lane logical budgets (priority, state, general, bulk,
  signer) plus a per-relay wire budget, partitioned into a reserved priority
  share, charged immediately before every EVENT frame including NIP-42 AUTH
  retries.
- Destination-aware receipt cache, in-flight suppression, bounded active
  publications and relay identities, generation-aware jittered breaker, file
  kill switch, content-free readiness details.
- Concord rotations and Direct Invite batches are admitted as one bounded,
  paced operation before the custody write.
- Durable outbox retries expire one hour after enqueue into the terminal
  `expired` state (migration `000071_nostr_publish_expired`).
- A type-aware guard rejects any raw publication outside exact gateways.

## Verification

- `go vet ./...` and `go test ./...` pass.
- `go test -race` passes for `nostrout`, `adapters/nostr`, `adapters/signet`,
  `soulfactory`, `controlplane`, and `repository`.
- Migration and repository integration tests (`-tags integration`) pass
  against `postgres:16-alpine`, including the full up/down round trip through
  `000071` and Postgres outbox expiry semantics.
- The guard rejects a deliberately planted method-value bypass of the RelayPool
  send hook and every disguised bypass in `internal/nostrout/testdata/bypass`.
- The repository Dockerfile builds from a clean worktree at
  `7c404ab2236c2110665096ccd5632331bf71b024`: image ID
  `sha256:e9ab8e1a0d80a6e58989f377b4d11b844aacabd768f3d60defd614298bcbf494`,
  OCI revision label equal to the source commit, non-root user `bahia`,
  `scripts/edge_image_admission.py verify-image` passes, and the embedded DNS
  agent reports `0.1.0-7c404ab2236c2110665096ccd5632331bf71b024`
  (sha256 `7e8af1a3052e0019b822d0a5e7b6c9b22b12dc946b260ba88b88cfa53b6020d6`).
  The build helper's host execution of the Linux agent binary is not portable
  to macOS; the equivalent checks were run with the agent inside a container.

## Remaining acceptance

Operator-authorized staged canary through the repository deployment workflow:
bounded event rate and zero steady-state relay rate-limit rejection over the
agreed window, real operator-result and tombstone delivery, restart hydration,
and a proven rollback — with no relay-limit, key, ACL, or custody change.
Production Bahia remains intentionally stopped until then.
