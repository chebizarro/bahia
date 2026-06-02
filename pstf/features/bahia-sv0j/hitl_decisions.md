# HITL Decisions — bahia-sv0j

Date: 2026-06-02

## Decision

Classify organization browser operations as `nostr_request_result_facade`.

## Rationale

The completed route matrix already classifies `/orgs/**` as an encrypted request/result facade. Repository evidence shows the web store publishes encrypted operation requests and the backend applies repository-backed RBAC/org changes before returning encrypted terminal results. There is no implemented public org read-model projection to justify `nostr_native` classification.

## Product impact

The UI and docs now describe org CRUD, membership, and invite operations as encrypted `5980`/`7980` request/result semantics. Durable org state remains repository-backed and private to encrypted results.

## Blockers

None recorded for this slice.
