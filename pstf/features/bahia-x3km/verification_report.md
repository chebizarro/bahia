# Verification Report: bahia-x3km

## Scope

- Fixed deterministic test setup for SecurityScanner SBOM Blossom payload resolution.
- Production SecurityScanner code was inspected; no production code change was required.

## Root Cause

`StorageResolver.Resolve` validates Blossom URI shape and delegates the full canonical Blossom URL to the Blossom client. The local `fakeBlossom` test client only looked up payloads by bare SHA-256 hash, so tests that configured `https://blossom.example/<hash>.json` saw missing-blob failures before reaching their intended assertions.

## Fix

`internal/service/security_scanner_test.go` now has the fake Blossom client derive lookup keys from the supplied canonical Blossom URL, including the hash basename with an optional extension stripped. This keeps tests local and deterministic while matching the production resolver/client seam.

## Evidence

- `GOCACHE=/private/tmp/bahia-go-cache go test ./internal/service` — passed on 2026-07-03.
- `go build ./...` — passed on 2026-07-03.

## Remaining Work

No remaining work identified for `bahia-x3km`.
