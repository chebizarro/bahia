# Phase 5 R3 worker/build/artifact CLI reads

Issue: `bahia-irsry.13.9`. Worktree: `p5-r3-workers`.

| Criterion | Evidence |
| --- | --- |
| Nostr default and REST parity | `TestR3ReadRESTNostrGolden` compares table and JSON output for workers list/show, builds get/list, and artifacts get/list against explicit `--http-fallback`; the default path makes no HTTP call. |
| Scoped worker reads | The canonical worker REQ selects the four worker `t` topics under the service author. A second REQ selects the Loom advertisement kind under worker pubkeys from canonical state or the explicit `show` argument, never the service key. The golden inspects both filters. |
| Decoder and tombstone handling | `TestR3RegistryDecodersRoundTrip` covers typed build, artifact, worker state, assignment, drain, and eligibility payloads plus tombstones. `TestR3WorkerAdvertisementUsesWorkerAuthorAndCursor` covers worker-authored content and tags. |
| Cursor and freshness | `TestR3BuildCursorAndStaleExit` verifies `since` reuse, cached output, warning, and successful stale exit. The golden verifies the worker-ad cursor also resumes. |
| Invalid and absent identities | `TestR3ReadMissingAndInvalidIDs` checks malformed build/artifact/worker IDs and missing artifact error behavior. |

Full Go gate passed with `CGO_ENABLED=0`: `go build ./...`, `go vet ./...`, and `go test ./...` (including `internal/archtest`). `gofmt` and `git diff --check` passed. The Beads server was unavailable (`beads_bahia` not found), and the task explicitly forbids writing `.beads/`, so the Beads issue state was not changed in this worktree.
