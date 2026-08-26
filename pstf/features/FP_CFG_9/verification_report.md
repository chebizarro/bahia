# FP_CFG_9 verification

## Verified behavior

- The API composes NIP-51 named people lists and NIP-78 policy events with registry-derived kinds and exact config coordinate tags.
- Publishing uses the existing operator Signet client and persists the signed event before relay publication.
- The durable Nostr event repository retains desired and applied/rejected status history for restart- and outage-safe drift reads.
- Rollback copies a prior validated desired payload and list items into a new event at the next author-coordinate version.
- Candidate policy content rejects secret-bearing field names and recognizable raw secret formats; schema-defined secret references remain supported.

## Quality gates

- `go build ./...` — passed.
- `go vet ./...` — passed.
- `go test ./...` — passed.
