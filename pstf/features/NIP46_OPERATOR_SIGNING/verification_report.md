# NIP-46 operator signing verification

The focused Go suites pass for `./cmd/cli`, `./pkg/client`, and
`./internal/adapters/nostr`.

The live Stew session connected to `bunker.sharegap.net`, passed the bunker
ping, resolved the remote operator public key, signed the ContextVM adoption
scan event, and reached the correlated-result subscription without loading a
local operator identity key.

The live Bahia control plane did not emit a terminal adoption-scan result
during the verification window on its configured ContextVM relays. That
runtime delivery/response observation is separate from remote signer
availability: the same probe previously failed before client construction
because the CLI had no NIP-46 signer path, and now advances through remote
signing and relay publication.

Follow-up tracing showed that the live runtime's sidecar precedence selected
an unresolvable public relay hostname instead of the configured ContextVM
relay set. With the embedded sidecar disabled, `controlPlaneRelayURLs` now
uses `ContextVMRelayPolicyRelays`; focused topology tests cover the configured
and browser-policy fallback cases. The Dockerfile also accepts an Athens
`GOPROXY` build argument and no longer mounts a repository credential.
