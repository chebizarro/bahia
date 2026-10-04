# Phase 5 R4 CLI confidential reads — verification

Issue: `bahia-irsry.13.10`. Branch: `feat/irsry-p5-r4-orgs`.

## Acceptance evidence

| Criterion | Evidence |
| --- | --- |
| Org list/get/members, secret metadata list, and notification channel list/get read signed relay state by default; REST is explicit | `TestCLIConfidentialRESTNostrGolden` compares the REST and Nostr commands in JSON and table modes, checks one scoped `#t`/author subscription per family and key-envelope family, and verifies HTTP is called only for `--http-fallback`. |
| The CLI signer unwraps real OCK envelopes without raw-key decryption | Fixture events are generated with `controlplane.EncryptConfidentialContent` and `MarshalOCKWrap` using deterministic service, member, and fleet keys; `TestCLIConfidentialNIP46Signer` exercises the bunker signer interface and its close lifecycle. |
| Non-members get a clear non-error result; fleet operators can read fleet metadata | `TestCLIConfidentialCursorStaleAndNonMember` checks `not readable with this key` and exit 0; the golden fixture includes an operator-wrapped synthetic `fleet` OCK and compares fleet channel output. |
| No secret values or notification credentials reach CLI output | Secret events carry only `SecretRef`; the golden test checks output excludes webhook URLs and signing secrets. Notification reads sanitize metadata again after decryption and never open `service_inner`. |
| Cursor reuse and stale exit behavior match R1/R2 | `TestCLIConfidentialCursorStaleAndNonMember` verifies both family cursors' `since` values, cached output, one stale warning, and successful exit. |
| Tampered signed coordinates cannot replay ciphertext | `TestCLIConfidentialRejectsSignedADTamper` re-signs an event with a different `d` tag and asserts the AEAD associated-data check fails. |

## Gate (2026-10-03)

- `CGO_ENABLED=0 go build ./...` — pass
- `CGO_ENABLED=0 go vet ./...` — pass
- `CGO_ENABLED=0 go test ./...` — pass on rerun. The first, high-load run hit the documented unrelated `internal/adapters/loom` relay-auth flake (`2 REQ filters, want 4`); its isolated retry passed without code changes.
- `CGO_ENABLED=0 go test ./internal/archtest -count=1 -run TestNoNew` — pass, zero added violations
- `gofmt` on touched Go files and `git diff --check` — pass

Beads status was not changed: `bd show bahia-irsry.13.10` could not connect to the worktree's Dolt database (`beads_bahia` absent on the local server), and the slice forbids writing `.beads/`.
