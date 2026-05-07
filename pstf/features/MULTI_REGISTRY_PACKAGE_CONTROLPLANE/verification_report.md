# MULTI_REGISTRY_PACKAGE_CONTROLPLANE verification report

## Phase

Item 2 backend abstraction and package service/policy core.

Item 1 foundation was completed in commit `9bb6298`.

## Verification status

Verified locally on 2026-05-06.

## Evidence

- `GOCACHE=/tmp/bahia-go-cache go test ./internal/backends/... ./internal/service` passed.
- `GOCACHE=/tmp/bahia-go-cache go test ./...` passed.
- Scope review: Item 2 changes are limited to `internal/backends/**`, `internal/service/package_registry*`, and PSTF updates. No package reactor handlers, MCP tools, CLI commands, API handlers, router entries, or REST CRUD endpoints were added.

## Item 2 behavior covered

- Added a pluggable package backend contract with capabilities advertising the requested formats: npm, pypi, conan, deb, rpm, pub, go_modules, and gradle.
- Added backend packages:
  - `internal/backends/filesystem_mock`
  - `internal/backends/nexus`
  - `internal/backends/pulp`
  - shared contract in `internal/backends/packagebackend`
  - construction compatibility layer in `internal/backends/factory`
- Added package service/policy logic for repository creation/deletion, artifact publish/store, promotion, yank, and drift-oriented observation helpers.
- Added deterministic tests for filesystem mock lifecycle, Nexus/Pulp HTTP skeleton behavior, service policy checks, digest/size verification, promotion approval, idempotent same-digest publish, yanking, and source overflow rejection.

## Current limitations / deferred work

This is not the full package control-plane capability. Reactor handlers, package command publishers, MCP tools, CLI commands, and Nostr workflow integration remain out of scope for Item 2.

Nexus and Pulp adapters are meaningful skeletons behind the interface, but production deployment details remain HITL decisions:

- exact Nexus API/version and auth wiring;
- exact Pulp file plugin/version and publication/distribution workflow;
- production secret/TLS resolver integration for `auth_secret_ref`, `tls_secret_ref`, and `secret_refs`.

The Item 2 factory explicitly rejects unwired secret refs instead of silently ignoring them.

## Review follow-up

Oracle review identified and this patch fixed four backend/service risks before completion:

- HTTP backend secret/TLS references are no longer silently ignored by the factory;
- Pulp repository ensure now propagates distribution setup failures;
- Nexus/Pulp no longer advertise checksum-level drift support they do not independently verify;
- source fetch verification is bounded by declared/policy size before writing unbounded bytes to disk.

`go test ./...` was rerun after these fixes and passed.
