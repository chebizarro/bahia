# BAHIA_NOSTR_AUDIT_PARITY verification

## Scope

Work item A for Beads `bahia-s6qp` and `bahia-5dlr`.

## Observed changes

- Expanded `DefaultInboundKinds` to cover the canonical control-plane request namespace: `5961-5968`, `5971-5976`, `5978-5979`, `5981-5989`, `5991-5996`, `38390-38394`, `7977`, and assistant prompt/approval request kinds.
- Added scoped subscriber author configuration for default, adoption, and direct-runtime operator pubkeys.
- Split generic subscriber filters into open, default control-plane, direct-runtime action, and adoption request filters.
- Wired app subscriber author scopes without folding adoption or direct-runtime-only pubkeys into the default scope.
- Wired `controlplane.WithNostrEventRepository` independently of package registry feature enablement.

## Verification

- `go test ./internal/adapters/nostr ./internal/app` — passed.

## Remaining issue scope

No remaining work is known for `bahia-s6qp` or `bahia-5dlr` in the touched scope.
