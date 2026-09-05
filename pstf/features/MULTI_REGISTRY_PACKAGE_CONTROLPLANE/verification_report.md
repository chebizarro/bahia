# MULTI_REGISTRY_PACKAGE_CONTROLPLANE verification report

## Phase

Item 3 signer-first Nostr control-plane and MCP surface wiring.

Item 1 foundation was completed in commit `9bb6298`.
Item 2 delivered control-plane plumbing plus skeleton Nexus/Pulp adapters in commit `8e2194f`; drift observation is not yet implemented for those adapters.

## Verification status

Control-plane plumbing and skeleton adapter behavior verified locally on 2026-05-06. This report does not claim production-complete Nexus/Pulp drift observation.

## Evidence

- `GOCACHE=/tmp/bahia-go-cache go test ./internal/controlplane ./internal/mcp ./internal/adapters/nostr ./internal/relaysidecar ./internal/app ./internal/domain ./internal/repository` passed.
- `GOCACHE=/tmp/bahia-go-cache go test ./...` passed.
- Scope review: Item 3 adds package reactor handlers, package command publisher, MCP tools, package event kind admission/auditing, app wiring, lifecycle migration, and focused tests. It does not add CLI package commands or REST CRUD routes.

## Item 3 behavior covered

- Added package lifecycle request/status/result/projection kinds for repository apply/delete, publication intent/upload, promotion, yank/deprecate, and drift detection.
- Added control-plane package handlers that:
  - run through the existing signer-first reactor dispatch path;
  - use event ID deduplication plus durable `request_event_id` package intent idempotency;
  - publish explicit `policy_check`, accepted/executing, replaceable registry, terminal result, and drift observation events;
  - keep package repository/artifact/promotion tables as projections/caches derived from signed events and backend observations.
- Added `PackageCommandPublisher` for MCP-originated signer-first package requests.
- Added MCP package tools:
  - signer-first mutations: repository apply/delete, upload, promote, yank/deprecate, drift detect;
  - projection reads: list, get, status.
- Wired package service/projection/command publisher into app startup when `packages.enabled=true`.
- Extended inbound Nostr subscriber and relay-sidecar policy to admit/audit package request and projection kinds.
- Extended promotion/publication status vocabulary with the required first-class statuses: pending, approved, running, succeeded, failed, rejected, rolled_back.

## Prior Item 2 control-plane plumbing and skeleton behavior retained

- Added a pluggable package backend contract with capabilities advertising the requested formats: npm, pypi, conan, deb, rpm, pub, go_modules, and gradle.
- Added backend packages:
  - `internal/backends/filesystem_mock`
  - `internal/backends/nexus`
  - `internal/backends/pulp`
  - shared contract in `internal/backends/packagebackend`
  - construction compatibility layer in `internal/backends/factory`
- Added package service/policy logic for repository creation/deletion, artifact publish/store, promotion, yank, and drift-oriented observation helpers.
- Added deterministic tests for filesystem mock lifecycle, Nexus/Pulp HTTP skeleton behavior, service policy checks, digest/size verification, promotion approval, idempotent same-digest publish, yanking, and source overflow rejection. These tests do not establish independent backend checksum/drift observation.

## Current limitations / deferred work

CLI package commands remain intentionally deferred to Item 4 per the user boundary.

Nexus and Pulp adapters are meaningful skeletons behind the interface. Both advertise `CanObserveDrift = false`; independent backend checksum retrieval and byte-level drift observation are not implemented. Production deployment details also remain unresolved:

- exact Nexus API/version and checksum workflow (`bahia-1zqhl`);
- exact Pulp file plugin/version and checksum/publication/distribution workflow (`bahia-1zqhl`);
- production authentication resolver integration for `auth_secret_ref` and `secret_refs` (`bahia-v8b6c`);
- production TLS resolver integration for `tls_secret_ref` (`bahia-rf5of`).

The Item 2 factory explicitly rejects unwired secret refs instead of silently ignoring them.

## Review follow-up

The original Oracle review identified four backend/service risks; the patch addressed them within the limited control-plane-plumbing and skeleton-adapter scope:

- HTTP backend secret/TLS references are no longer silently ignored by the factory;
- Pulp repository ensure now propagates distribution setup failures;
- Nexus/Pulp no longer advertise checksum-level drift support they do not independently verify;
- source fetch verification is bounded by declared/policy size before writing unbounded bytes to disk.

`go test ./...` was rerun after these fixes and passed.
