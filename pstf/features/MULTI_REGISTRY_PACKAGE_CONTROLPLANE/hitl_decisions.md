# HITL decisions: MULTI_REGISTRY_PACKAGE_CONTROLPLANE

## Recorded decisions

- User brief overrides the Oracle plan's generic-only package-format suggestion. Item 1 preserves npm, pypi, conan, deb, rpm, pub, go_modules, and gradle as first-class values.
- Backend credentials must be references only (`auth_secret_ref`, `tls_secret_ref`, `secret_refs`), not inline password/token/private-key values.
- Item 2 keeps Nexus/Pulp auth secret resolution out of service/backend core; the factory rejects configured secret refs until a production secrets/TLS resolver is wired.
- PostgreSQL tables are projection/cache tables only; Nostr events remain authoritative desired state.
- Item 3 keeps package MCP mutations receipt-returning and signer-first; final state is observed through package status/result events and projection-backed `list/get/status` tools.

## Human decisions needed for later items

- Nexus API/version and exact raw-hosted repository endpoints.
- Pulp plugin/version, task response schema, and publication/distribution workflow.
- Production default `packages.allowed_source_hosts` policy.
- Exact promotion channels/environments and approval policy semantics.
- Whether additional package formats or a raw/generic format should be added in a later compatibility phase.
- Whether Item 4 should automatically replay non-terminal long-running package uploads on process restart or require a fresh signed operator intent.

## bahia-lxc2q (2026-09-24)

The explicit remediation request authorizes resolving approval provenance and
yank/deprecate semantics before exposure. Follow the VM ApprovePlan model:
server-reconstructed plan and resource snapshot, distinct authenticated fleet
operator, ten-minute expiry and atomic one-use consumption. Local security
admission records are authoritative even though resource tables remain projections.
Deprecation is advisory metadata preserving access; explicit yank remains
destructive. No package-manager-native deprecation capability is inferred.
Interrupted operations remain claimed and require inspection before a fresh intent;
there is no automatic retry of unknown backend outcomes. CLI/MCP approval-field
exposure and repository apply/delete registration remain outside this task.
