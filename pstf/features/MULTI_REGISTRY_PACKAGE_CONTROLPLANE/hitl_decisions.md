# HITL decisions: MULTI_REGISTRY_PACKAGE_CONTROLPLANE

## Recorded decisions

- User brief overrides the Oracle plan's generic-only package-format suggestion. Item 1 preserves npm, pypi, conan, deb, rpm, pub, go_modules, and gradle as first-class values.
- Backend credentials must be references only (`auth_secret_ref`, `tls_secret_ref`, `secret_refs`), not inline password/token/private-key values.
- PostgreSQL tables are projection/cache tables only; Nostr events remain authoritative desired state.

## Human decisions needed for later items

- Nexus API/version and exact raw-hosted repository endpoints.
- Pulp plugin/version and task response schema.
- Production default `packages.allowed_source_hosts` policy.
- Exact promotion channels/environments and approval policy semantics.
- Whether additional package formats or a raw/generic format should be added in a later compatibility phase.
