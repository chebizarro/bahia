# MULTI_REGISTRY_PACKAGE_CONTROLPLANE verification report

## Phase

Item 1 foundation scaffold only.

## Verification status

Verified locally on 2026-05-06.

## Evidence

- `go test ./internal/domain ./internal/config ./internal/repository` passed.
- `go test ./...` passed.
- Scope review: changes are limited to domain/config/repository/migrations/tests/PSTF plus Beads metadata; no package reactor handlers, MCP tools, CLI package commands, backend adapters, or REST CRUD endpoints were added.

## Current limitations

This is not the full package control-plane capability. Reactor handlers, package command publishers, MCP tools, CLI commands, and package backend adapters remain out of scope for Item 1.

## Review follow-up

Oracle review identified and this patch fixed three projection/idempotency risks before commit:

- terminal package intents are no longer downgraded by replayed non-terminal request upserts;
- partial intent upserts preserve cached request/result payloads instead of overwriting them with JSON null;
- artifact logical identity upserts no longer rotate the artifact primary key on conflict.

`go test ./...` was rerun after these fixes and passed.
