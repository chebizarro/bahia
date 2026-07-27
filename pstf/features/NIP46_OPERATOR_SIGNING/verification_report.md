# NIP-46 operator signing verification

The focused Go suites pass for `./cmd/cli`, `./pkg/client`, and
`./internal/adapters/nostr`.

The live Stew session connected to `bunker.sharegap.net`, passed the bunker
ping, resolved the remote operator public key, signed the ContextVM adoption
scan event, and reached the correlated-result subscription without loading a
local operator identity key.

The initial live Bahia control plane did not emit a terminal adoption-scan
result. Tracing showed that this was a runtime topology and tier-gating issue,
not a remote signer failure. After the fixes below were deployed, the same
NIP-46 session completed signed adoption scan and import operations without
loading a local operator identity key.

Follow-up tracing showed that the live runtime's sidecar precedence selected
an unresolvable public relay hostname instead of the configured ContextVM
relay set. With the embedded sidecar disabled, `controlPlaneRelayURLs` now
uses `ContextVMRelayPolicyRelays`; focused topology tests cover the configured
and browser-policy fallback cases. The Dockerfile also accepts an Athens
`GOPROXY` build argument and no longer mounts a repository credential.

Live rollout then exposed that production's active Tier 1 gated the ContextVM
request transport because it had been registered as Tier 2. The ContextVM
transport is Bahia's canonical mutation plane and is now registered as Tier 1;
nonessential projection/reconciliation runners retain their existing tiers.

The final live acceptance imported the healthy `fleet-athens` workload as the
Bahia service `fb7ba58b-6ffb-48da-8db3-e1bf9c847cfe`. Read-only database
verification matched the adopted runtime identity to container
`8027a699918faca37954d139527cbe92b2de48990e222193517577c2b228aace` and the
pinned image digest
`sha256:cbf1a5e4649a06b6d0089088aa47880af7908f908716f679aa9ac842dfe60b85`.
Bahia and Athens remained healthy after adoption.

A clean GitHub checkout of Bahia also built through the authenticated Athens
proxy using only `GOPROXY` and `GONOSUMDB`; no local Cascadia worktree,
developer token, or build-time repository credential was present. A
post-adoption fetch of `cascadia-go@v1.0.2` returned its canonical version
metadata and module archive with HTTP 200.
