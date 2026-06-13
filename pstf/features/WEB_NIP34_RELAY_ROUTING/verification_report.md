# WEB_NIP34_RELAY_ROUTING Verification Report

## Beads

- Issue: `bahia-dq6k` — Route services repository NIP-34 queries to advertised NIP-34 relays

## Evidence Plan

- Verify repository store resolves `nostr.nip34_relays` and never calls `nostr.connect` for repository discovery.
- Verify `fetchRepositories` passes explicit relay URLs to the Nostr pool query for kind `30617` filters.
- Verify sidecar policy treats all NIP-34 kinds from the local `nips/34.md` spec as readable open interop kinds and accepts signed repository announcement publishes.
- Verify generated frontend kind constants remain in sync with backend constants.

## Results

### Go sidecar and kind policy

Command:

```sh
go test ./internal/kinds ./internal/relaysidecar
```

Result:

- PASS: `github.com/openagentsinc/bahia/internal/kinds`
- PASS: `github.com/openagentsinc/bahia/internal/relaysidecar`

Coverage exercised:

- `IsOpenInteropKind` recognizes NIP-22 comment kind `1111` plus all NIP-34 kinds from local `nips/34.md`.
- `IsReadableKind` treats NIP-22 comments plus NIP-34 repository and issue kinds as readable.
- Generated frontend kind constants remain drift-checked against `internal/kinds/kinds.go`.
- Sidecar `OnRequest` accepts read filters for NIP-22 comments and all NIP-34 kinds.
- Sidecar accepts a signed kind `30617` repository announcement from a non-service publisher as open interop data.

### Web repository relay routing

Command:

```sh
cd web && pnpm exec vitest run --config vitest.config.js tests/unit/discovery-store.test.js tests/unit/repositories-store.test.js tests/unit/repositories-nip34.test.js
```

Result:

- PASS: 3 files, 23 tests.

Coverage exercised:

- System discovery preserves and normalizes advertised `nostr.nip34_relays` from trusted discovery events.
- Repository store resolves configured `nostr.nip34_relays`, dedupes and trims values, and passes them to `fetchRepositories`.
- Repository store does not call `nostr.connect` during repository discovery.
- Repository store loads relay policy from `loadSystemInfo()` when cached system info is absent.
- Repository store reloads when advertised NIP-34 relay policy changes.
- Repository store falls back to the global relay pool only when relay discovery fails.
- `fetchRepositories` forwards explicit relay URLs to `queryOrPartial` for scoped kind `30617` filters.

### Web lint

Command:

```sh
cd web && pnpm lint
```

Result:

- PASS: `svelte-check found 0 errors and 0 warnings`.

## Review Disposition

A review pass found that discovery normalization dropped `nostr.nip34_relays`, the repository cache ignored relay-policy changes, and NIP-34 replies require NIP-22 comment kind `1111`. The implementation was updated before final verification to preserve discovery relay policy, reload repository discovery when the relay set changes, and include NIP-22 comments in sidecar open interop policy.

## Conclusion

`services/` repository discovery no longer depends on the browser sidecar when `nostr.nip34_relays` are advertised, and the Bahia sidecar no longer rejects NIP-34 collaboration kinds or NIP-22 replies as unreadable. The remaining `service secrets via ContextVM requests: method not found` console error from the user log is outside this NIP-34 relay-routing slice and is tracked separately as `bahia-9m1d`.
