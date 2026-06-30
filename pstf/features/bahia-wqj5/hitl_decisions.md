# HITL Decisions — bahia-wqj5

## Decision Log

- `HITL-SBOM-WQJ5-001` (2026-06-30): Canonical immutable SBOM subject locator shapes are approved as follows:
  - Package subjects that omit `subject.digest` use `subjectLocator.package` with `repository_id`, optional `namespace`, `package_name`, `version`, `filename`, and required `sha256`. Bahia resolves the package artifact projection by those coordinates and requires the projected SHA-256 to match the locator before deriving `subject.digest=sha256:<sha>`.
  - Repository subjects that omit `subject.digest` use `subjectLocator.repository` with either `commit` or `content_digest`, not both. A commit resolves to `subject.digest=git:<commit>` after 40/64-hex validation. A content digest must use `sha256:<64-hex>` form.
  - Mutable package names, versions without artifact SHA-256, repository names, and repository URLs alone are not durable SBOM subject truth.

## Evidence

- `docs/designs/sbom-real-support.md` now documents `subjectLocator` for `sbom/generate` and applies the same request shape to `sbom/import` inputs.
- `docs/user-guide/features/packages.md` documents package artifact locator fields.
- `docs/user-guide/nostr-integration.md` documents package and repository locator examples for ContextVM SBOM intents.
