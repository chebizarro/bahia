# Entity identity: author-minted UUIDs

For every domain whose identity is not inherent in a natural key, **the author
of the create mints an RFC 9562 UUID and fixes it at creation**. The id is the
entity segment of the addressable coordinate (`<prefix>:<uuid>` or a bare
`<uuid>`), so a rename never moves the coordinate and a retry reuses the same
coordinate. Normative text: `docs/event-spec.md` "Entity identity and
coordinates"; code: `internal/domain/entity_id.go`.

Rules:

- New ids are **UUIDv7** (time-ordered). A create intent may carry a UUIDv4
  (what `crypto.randomUUID` and older rows produce). Anything else is rejected
  with `ErrInvalidEntityID`.
- Natural-key coordinates exist only where the key *is* the identity and is
  never renamed: DNS zones and backends (`zone:<name>`), content-addressed
  SBOM and security references, and request-event-id-correlated state.
- Natural keys such as `(org, name)` are **uniqueness constraints enforced by
  the authoritative applier**, never the identity. Service, environment, policy
  and LLM-route names are fleet-wide unique because the DNS projector maps
  environment names to zones and service names to labels. The first applied
  create wins, ordered by `(created_at, event id)`.
- A create resolves **by id, then by content** (`resolveCreateByID`):
  no entity → create; same content → idempotent replay with no write and no
  republish; different content → `domain.ErrEntityIDConflict` (ContextVM
  JSON-RPC `-32010`, intent status `rejected`). The stored org is part of the
  compared content, so an id cannot reach into another org, and relays scope
  addressable events by author pubkey, so no signer can overwrite another's
  coordinate.
- Daemon-authored records (backup jobs, package publications, ML versions,
  security runs, adoption bindings) are minted the same way in code; where the
  id derives from the request (security runs from schedule id and due time,
  adoption from the import request) a retry re-addresses the same coordinate.

Producers that implement the rule end to end: web dialogs mint once per
attempt, the CLI accepts `--id` / `--idempotency-key`, MCP create tools mint
when the caller supplies no id, and the services and handlers classify
primary-key hits as replay or conflict before publishing. Postgres `id uuid`
columns store the value verbatim; their `DEFAULT gen_random_uuid()` is unused.
