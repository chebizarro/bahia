# FP_CFG_9 verification

## Verified behavior

- The API composes NIP-51 named people lists and NIP-78 policy events with registry-derived kinds and exact config coordinate tags.
- Publishing uses the existing operator Signet client and persists the signed event before relay publication.
- The durable Nostr event repository retains desired and applied/rejected status history for restart- and outage-safe drift reads.
- Rollback copies a prior validated desired payload and list items into a new event at the next author-coordinate version.
- Candidate policy content rejects secret-bearing field names and recognizable raw secret formats; schema-defined secret references remain supported.
- The Admin navigation exposes a working Config Fabric console backed by the existing drift, publish, and rollback APIs.
- Drift rows render desired/applied event IDs and versions, drift, and the latest rejection; policy detail renders retained desired/effective payloads, status audit history, and version history.
- Publish/edit uses the generated Nostr kind constants and performs client-side coordinate, schema, monotonic version, list item, policy, secret reference, and raw-secret validation before API submission.
- Rollback requires operator confirmation and republishes a selected retained version through the existing server Signet-backed API.

## Quality gates

- `cd web && npm run lint` — passed with 0 errors and 0 warnings.
- `cd web && npm run test:unit` — passed: 92 files, 698 tests.
- `cd web && npm run build` — passed; adapter-static wrote the production site.
- `go build ./...` — passed.
- `go vet ./...` — passed.
- `go test ./...` — passed.
