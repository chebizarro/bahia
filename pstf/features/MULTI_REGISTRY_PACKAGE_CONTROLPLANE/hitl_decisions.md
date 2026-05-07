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
