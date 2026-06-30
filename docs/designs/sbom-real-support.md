# Real SBOM Support Design

Date: 2026-06-13

## Goal

Make SBOM support in Bahia production-real for newly created or imported deployments, package artifacts, repositories, and service artifacts:

- generate SBOM payloads with real generators, primarily Anchore Syft and optionally cdxgen;
- store generated/imported SBOM payload bytes on Blossom;
- publish durable Nostr observables for SBOM references and availability;
- preserve non-Nostr interoperability only where it is needed;
- keep searchable local projections for UI/API convenience without treating the database as canonical truth.

## Current repository evidence

Existing reusable code:

- `internal/adapters/sbom/parser.go` parses SPDX JSON and CycloneDX JSON into artifact SBOM metadata and package rows.
- `internal/adapters/sbom/attestation.go` builds in-toto-style SBOM attestations and verifies subject/payload digests.
- `internal/adapters/sbom/storage.go` uploads/downloads SBOM payloads through Blossom and verifies payload hashes.
- `internal/api/handlers/sbom.go` exposes REST read/search and artifact SBOM ingest endpoints.
- `internal/repository/pg_sbom.go` persists artifact-scoped SBOM projections and package indexes.

Observed gaps:

- There is no real SBOM generation pipeline.
- Existing data model is artifact-specific, not subject-neutral.
- Existing REST ingest parses/stores directly and bypasses Blossom/Nostr publication.
- `internal/adapters/sbom/index.go` treats `30079` as a NIP-51-style list even though `docs/nostr-event-implementation-guide.md` says NIP-51 collections should use real list kinds such as `30004`, while `30078` is NIP-78 app data.
- `AddToIndex` currently replaces with only a new entry instead of merging a complete subject list.
- Direct OCI/package-backend SBOM storage is explicitly not implemented; generated payloads should use Blossom.

## Non-goals

- Do not add REST generation endpoints unless a concrete non-Nostr integration requires them.
- Do not store full SBOM payloads in Nostr events.
- Do not make `30079` canonical.
- Do not implement direct OCI/package-backend SBOM writes in this feature; keep those as follow-up work if required.

## Generator strategy

### Syft default

Use `github.com/anchore/syft/syft` as the default in-process generator for:

- OCI/container images;
- local directories and repository checkouts;
- package archives or filesystem artifacts;
- SPDX JSON output;
- CycloneDX JSON output.

Syft is the default because it is Go-native, can be used as a library, and supports broad container/filesystem/package cataloging with SPDX, CycloneDX, and Syft-native formats.

### cdxgen optional adapter

Use cdxgen as an optional external executable adapter for source repositories where richer CycloneDX metadata is beneficial.

Rules:

- cdxgen is used only for CycloneDX output.
- `generator: "cdxgen"` fails clearly if the configured binary is unavailable.
- `generator: "auto"` may choose cdxgen for repository checkout + CycloneDX when enabled; otherwise it falls back to Syft.
- cdxgen should not be a Go module dependency; configure the binary path.

## Subject model

Add a subject-neutral SBOM model in `internal/domain/sbom.go`.

Subject types:

- `artifact`
- `deployment`
- `package`
- `repository`

A subject has:

- type;
- stable ID;
- optional display name;
- required digest in `algo:value` form.

Digest conventions:

- artifacts/packages/images: `sha256:<hex>` when content-addressed;
- repositories: `git:<commit-sha>` unless an archive/content digest is available;
- deployments: `sha256:<hash-of-rendered-desired-state>` or a documented equivalent once existing deployment state code is inspected.

## Canonical flow

```text
ContextVM 25910 intent (`sbom/generate` or `sbom/import`)
  -> validate request shape and idempotency key
  -> enqueue SBOM orchestration and return an accepted acknowledgment with the status d-tag
  -> background runner publishes 30315 accepted/running status
  -> generate or resolve SBOM bytes
  -> parse SBOM bytes into package metadata
  -> upload exact bytes to Blossom
  -> verify Blossom descriptor/hash matches payload SHA-256
  -> build in-toto SBOM attestation
  -> publish 30078 SBOM reference app-data event and verify relay OK
  -> publish/replace 30004 NIP-51 availability list and verify relay OK
  -> store local projection and package index
  -> publish 30315 completed status and 4903 audit fact
```

Failure behavior:

- ContextVM handler failure means the intent was not accepted or enqueued;
- generation/import resolution failure publishes failed status and no completion;
- Blossom upload failure publishes failed status and no SBOM reference/list;
- Nostr publish rejection leaves the projection draft/failed and does not publish completed status;
- database projection failure after relay publication emits audit/status evidence because relay events are canonical.

## Nostr events

### SBOM reference event

Kind: `30078` (NIP-78 app data)

Meaning: detailed SBOM reference/attestation record. The content is the existing in-toto-style `domain.SBOMAttestation` envelope, not the SBOM payload.

Stable `d` coordinate:

```text
sbom:ref:<subject-key>:<format>:<payload-sha256>
```

`subject-key` is a deterministic hash of subject type, subject ID, and subject digest.

Required tags:

```text
["d", "sbom:ref:<subject-key>:<format>:<payload-sha256>"]
["domain", "sbom"]
["schema", "bahia.sbom.ref.v1"]
["subject_type", "artifact|deployment|package|repository"]
["subject", "<algo:value>"]
["format", "spdx|cyclonedx"]
["storage", "blossom"]
["location", "<blossom-url>"]
["x", "<payload-sha256>"]
["media_type", "<iana-media-type>"]
["generator", "<id>@<version>"]
["ntia", "compliant|partial|unknown"]
```

Add resource tags when applicable: `artifact`, `deployment`, `package`, `repo`.

Validation requirements:

- `x` equals the SHA-256 of the exact Blossom payload bytes.
- attestation `predicate.digest.sha256` equals `x`.
- attestation subject digest equals the `subject` tag.
- publication result checks relay OK accepted flag and rejection message.

### SBOM availability list

Kind: `30004` (NIP-51 app/list collection)

Meaning: complete replaceable list of available SBOM manifests for one subject version.

Stable `d` coordinate:

```text
sbom:available:<subject-type>:<subject-key>
```

Required list tags:

```text
["d", "sbom:available:<subject-type>:<subject-key>"]
["title", "SBOMs for <subject-name-or-id>"]
["domain", "sbom"]
["schema", "bahia.sbom.available-list.v1"]
["subject_type", "artifact|deployment|package|repository"]
["subject", "<algo:value>"]
```

Each entry includes:

```text
["a", "30078:<publisher-pubkey>:<ref-d>"]
["sbom", "<subject-digest>", "<format>", "blossom", "<location-uri>", "<payload-sha256>", "<generator-id>", "<ref-d>"]
```

List update rules:

- replace the complete list;
- serialize updates per subject;
- dedupe by subject type + subject ID + subject digest + format + generator ID + payload SHA-256;
- do not publish a single-entry list if other published entries for the subject exist.

### Status events

Kind: `30315` (NIP-38 status)

Use a `d` coordinate such as:

```text
sbom:run:<idempotency-key-or-run-id>
```

Statuses: `accepted`, `running`, `completed`, `failed`.

Expected steps:

- `accepted`
- `resolving_subject`
- `generating`
- `parsing`
- `uploading_to_blossom`
- `publishing_reference`
- `publishing_available_list`
- `projecting`
- `completed`

Completion truth remains the `30078` and `30004` events, not status alone. The ContextVM JSON-RPC response for `sbom/generate` and `sbom/import` is an acknowledgment only; it is not completion proof.

### Audit events

Kind: `4903`

Use for immutable facts:

- `sbom.generated`
- `sbom.imported`
- publish failures or projection divergence when security-relevant.

### Legacy kind

`30079` is legacy-only. Keep read compatibility if existing relay history/tests require it, but do not publish it as canonical SBOM availability state.

## ContextVM methods

### `sbom/generate`

Mutation intent for Bahia-controlled generation.

Inputs:

- `idempotencyKey`;
- `subject`;
- optional `subjectLocator` for immutable package/repository subject digest resolution when `subject.digest` is not supplied;
- `source` locator/kind;
- requested `formats`;
- `generator`: `auto`, `syft`, or `cdxgen`;
- `storage`: must be `blossom` for generated SBOMs.

Package subjects that omit `subject.digest` must use package artifact coordinates and SHA-256:

```json
"subject": { "type": "package" },
"subjectLocator": {
  "package": {
    "repository_id": "<package-repository-uuid>",
    "namespace": "@company",
    "package_name": "utils",
    "version": "1.2.3",
    "filename": "utils-1.2.3.tgz",
    "sha256": "<package-archive-sha256>"
  }
}
```

Repository subjects that omit `subject.digest` must use either an immutable git commit or an immutable SHA-256 content digest:

```json
"subject": { "type": "repository" },
"subjectLocator": {
  "repository": {
    "repository_url": "https://git.example/company/api.git",
    "commit": "<40-or-64-hex-git-object-id>"
  }
}
```

or:

```json
"subject": { "type": "repository", "id": "company/api" },
"subjectLocator": {
  "repository": { "content_digest": "sha256:<repository-archive-digest>" }
}
```

Large payloads do not belong inside ContextVM content.

Response:

```json
{
  "accepted": true,
  "status": "accepted",
  "run_id": "<idempotencyKey>",
  "status_d_tag": "sbom:run:<sanitized-idempotencyKey>",
  "idempotencyKey": "<idempotencyKey>",
  "observable_kinds": [30315, 4903, 30078, 30004]
}
```

The response means Bahia accepted the idempotent intent into the SBOM async runner. Clients must subscribe to `30315` by `#d=<status_d_tag>` for progress and to subject-scoped `30078`/`30004` for terminal canonical truth.

### `sbom/import`

Mutation intent for importing an existing SBOM from a source reference.

Inputs:

- `idempotencyKey`;
- `subject`;
- declared or auto-detected format;
- source reference, existing Blossom URL, package artifact source, or REST compatibility upload reference;
- `storage`: `blossom` unless already verified on Blossom.

Imported SBOMs still produce the same `30078` reference and `30004` availability list. `sbom/import` returns the same accepted acknowledgment shape as `sbom/generate`; clients must not block on or infer completion from the ContextVM response.
## REST API stance

Keep existing REST read/search endpoints for UI and non-Nostr read-model interoperability.

Keep `POST /artifacts/{id}/sbom` only as a non-Nostr import compatibility endpoint. Rewire it to the SBOM import service so it uploads to Blossom and publishes Nostr observables. Do not add REST generation endpoints unless an implementation bead records a concrete interoperability requirement.

## Persistence

Add generalized projection tables:

- `sbom_manifests`
- `sbom_manifest_packages`

Track:

- subject type/ID/name/digest;
- format/media type;
- Blossom storage URI and payload SHA-256;
- generator ID/version/pubkey;
- package and vulnerability counts;
- NTIA metadata;
- Nostr reference/list event IDs and `d` coordinates;
- publish state: draft/published/failed;
- source kind: generated/imported/external;
- timestamps and metadata.

Keep writing existing `artifact_sboms` and `sbom_packages` compatibility projections for artifact subjects until API/UI migrate.

## Implementation slices

1. Protocol and docs: constants, event guide updates, `30079` legacy marking.
2. Domain and DB: subject-neutral domain types, migrations, repository interfaces.
3. Parser refactor: subject-neutral parsing while preserving artifact API.
4. Generators: common interface, Syft adapter, optional cdxgen adapter.
5. Nostr publisher: `30078` reference and `30004` availability list builders with OK verification contract.
6. SBOM service: generation/import orchestration, idempotency, subject locking, status/audit publication.
7. ContextVM integration: register `sbom/generate` and `sbom/import` handlers.
8. REST compatibility: route existing artifact SBOM ingest through service import, preserve reads.
9. Tests/PSTF/docs: deterministic unit/integration tests, no sleeps, update PSTF and user docs.
10. Verification epic: verify every preceding epic against this design and PSTF, close or create follow-up beads.

## Acceptance criteria

- SBOM generation for an artifact/package/repository subject uses a real generator and yields valid SPDX or CycloneDX JSON.
- Generated/imported payload bytes are uploaded to Blossom and digest-verified.
- A `30078` SBOM reference event is published only after relay OK acceptance.
- A `30004` NIP-51 availability list is published/replaced with all known entries for the subject and relay OK acceptance.
- `30079` is not used for new canonical publication.
- ContextVM `sbom/generate` and `sbom/import` publish event-driven status and terminal observables; no polling or timeout-based completion is introduced.
- Existing REST reads still work; existing REST ingest becomes a compatibility import path that does not bypass Blossom/Nostr.
- Tests map to PSTF acceptance criteria and inject/generate deterministic EVENT/EOSE/OK/CLOSED/AUTH-style behavior where relevant.
- Documentation is updated for Nostr events, artifacts/packages/deployments, CLI/MCP only if changed.

## Ambiguities to resolve during implementation

- Exact deployment subject digest source in current deployment state code.
- Exact ContextVM method registration path.
- Whether Bahia runtime images should bundle cdxgen or require an operator-configured executable.
- Whether existing publish interfaces already verify per-relay OK or need replacement/wrapping.
- Whether the artifact SBOM UI should continue REST reads for now or move to direct Nostr read-model subscriptions in this feature.
