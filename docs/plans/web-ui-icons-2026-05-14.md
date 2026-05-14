# Web UI Icons — Plan

> **Status**: Ready for implementation
> **Date**: 2026-05-14
> **Scope**: Bahia web app iconography for services, artifacts, environments, deployment cross-references, detail pages, and dialogs

---

## Goal

Add consistent, non-emoji web icons to Bahia UI surfaces where users identify services, artifacts, environments, and related detail/dialog contexts. This is a presentational web-layer change only: no backend, store, Nostr event, schema, or route-behavior changes.

## Background

- The web app currently has no icon package dependency; `web/package.json:18-35` lists Svelte/Vite tooling plus app dependencies only.
- Shared empty states accept a string `icon` prop and default to emoji in `web/src/lib/components/EmptyState.svelte:1-17`.
- Tables render `col.render(row)` as HTML strings today, so entity icons need a backward-compatible column contract rather than a table rewrite: `web/src/lib/components/Table.svelte:1-31`.
- Cards and dialogs are text-oriented today: `web/src/lib/components/Card.svelte:1-20`, `web/src/lib/components/Modal.svelte:1-85`, and `web/src/lib/components/ConfirmDialog.svelte:1-39`.
- Current route emoji/icon hotspots include services empty states (`web/src/routes/services/+page.svelte:239-289`), Artifact/Blossom tabs and MIME icons (`web/src/routes/artifacts/+page.svelte:144-261`), environment protected/drift states (`web/src/routes/environments/+page.svelte:43-132`, `web/src/routes/environments/[id]/+page.svelte:265-331`), and deployment cross-reference tables (`web/src/routes/deployments/+page.svelte:98-145`).
- Prior verified polish work improved navigation, dashboard affordances, and logo sizing, but did not establish a reusable icon system: `pstf/features/WEB_NAV_DASHBOARD_POLISH/verification_report.md:5-15` and `pstf/features/WEB_DASHBOARD_DETAIL_INTERACTIONS/verification_report.md:5-16`.

## Icon Set

Use **Tabler Icons for Svelte** via `@tabler/icons-svelte`.

Rationale: Tabler has an official Svelte component package, ES module tree-shaking, stroke-based SVGs, and broad enough coverage for entity, status, file/media, and maritime-adjacent metaphors. Lucide is a viable fallback, but Tabler's larger vocabulary is a better fit for Bahia's harbor/maritime theme. Heroicons lacks an official Svelte package, and Material Symbols is primarily font-oriented.

## Approach

Implement a small shared icon layer, then update targeted routes.

### 1. Shared icon alias module

Create `web/src/lib/icons/domain-icons.js`.

Use this module as the only import source for route-level icons. It should:

- import Tabler Svelte components;
- export semantic aliases such as `ServiceIcon`, `ArtifactIcon`, `EnvironmentIcon`, `DeploymentIcon`, `RollbackIcon`, `BlossomIcon`, `ProtectedIcon`, `WarningIcon`, `SuccessIcon`, `UnknownIcon`, `CopyIcon`, `SbomIcon`, and `SignatureIcon`;
- export `blossomContentTypeIcon(contentType)`, returning a component reference for image, video, audio, JSON, text, PDF, and generic-file fallbacks;
- keep exact Tabler export-name validation isolated to this one file.

Use maritime-adjacent icons only where the meaning stays obvious. For example, an anchor/ship/lighthouse-style icon is appropriate for services or environments only if it remains legible at table/header sizes; otherwise prefer generic server/package/target icons.

### 2. Shared component contracts

Use explicit additive props instead of overloading existing behavior.

| Component | Contract |
|---|---|
| `EmptyState.svelte` | Add `iconComponent`; keep legacy string `icon` fallback. Prefer `iconComponent={ServiceIcon}` etc. Default should become a non-emoji generic empty icon. |
| `Table.svelte` | Keep existing `render(row)` HTML-string path untouched. Add optional `icon` and `text` column fields: `icon` may be a component or `(row) => Component`; `text` may be `(row) => string`. If `icon` is present, render icon + text; otherwise use current behavior. |
| `Card.svelte` | Add optional `titleIcon` before the title; keep `value` plain text. |
| `Modal.svelte` | Add optional `titleIcon` beside the modal title. |
| `ConfirmDialog.svelte` | Add optional `titleIcon` and forward it to `Modal`. |

Accessibility rules:

- Decorative icons should use `aria-hidden="true"`.
- Text labels remain the accessible source of truth.
- Icon-only controls, especially copy actions, must keep explicit `aria-label`s.

### 3. Route updates

| Area | Files | Add icons to |
|---|---|---|
| Services | `web/src/routes/services/+page.svelte`, `web/src/routes/services/[id]/+page.svelte` | Page/detail titles, service table cells, empty/filter-empty states, service/artifact/secret section headers, artifact rows, and Create/Edit/Delete/Deploy/Rollback/Secret/Register Artifact dialog titles. |
| Artifacts | `web/src/routes/artifacts/+page.svelte`, `web/src/routes/artifacts/[id]/+page.svelte`, `web/src/lib/components/SBOMDetails.svelte` | Artifact titles, Registry/Blossom tabs, artifact/service table cells, Blossom MIME type cells, SBOM/signature tabs, digest copy affordance, signature status/empty states, and SBOM storage-type labels. |
| Environments | `web/src/routes/environments/+page.svelte`, `web/src/routes/environments/[id]/+page.svelte` | Environment titles, environment table cells, protected column, environment detail card titles/statuses, deployed-service/history sections, service/artifact cells, empty states, and Create/Edit/Delete/Service Detail dialog titles. |
| Deployments | `web/src/routes/deployments/+page.svelte`, `web/src/routes/deployments/[id]/+page.svelte` | Deployment titles, service/environment/artifact cells, status cards, run/activity sections, empty/filter-empty/not-found/error states, and Rollback/Approve/Reject dialog titles. |

Non-goals:

- Do not iconize every form label or every badge.
- Do not change data loading, row-click navigation, modal lifecycle, command publishing, stores, or API clients.
- Do not add a second icon package.

## Work Items

1. **Install and lock the icon package**
   - Add `@tabler/icons-svelte` to `web/package.json`.
   - Run install from `web/` and commit the updated lockfile.

2. **Add the domain icon module**
   - Create `web/src/lib/icons/domain-icons.js`.
   - Export semantic aliases and `blossomContentTypeIcon(contentType)`.
   - Build once after this step to catch invalid Tabler export names early.

3. **Extend shared primitives**
   - Update `EmptyState`, `Table`, `Card`, `Modal`, and `ConfirmDialog` using the contracts above.
   - Keep all existing callers valid before route conversion.
   - Add unit tests for `EmptyState` component icons, `Table` icon columns, and Blossom MIME mapping.

4. **Convert list pages first**
   - Update services, artifacts, environments, and deployments list routes.
   - Exercise the new `Table` icon/text column path and `EmptyState` `iconComponent` path.
   - Replace the route-local Blossom MIME emoji mapper with `blossomContentTypeIcon`.

5. **Convert detail pages and dialogs**
   - Update service, artifact, environment, deployment detail routes, plus `SBOMDetails.svelte` storage-type labels.
   - Move emoji status markers out of card values and into title/status icon slots where possible.
   - Add dialog title icons for the scoped create/edit/delete/deploy/rollback/approve/reject/reveal/register flows.

6. **Sweep targeted emoji/glyph leftovers**
   - Search touched files for UI-provided emoji/glyph icons such as `📦`, `🔍`, `🌸`, `🌍`, `🔒`, `⚠️`, `✅`, `❓`, `📋`, `🔏`, and `🚀`.
   - Convert only planned UI icons; leave user/content data alone.

7. **Verify**
   - Run `npm run build` from `web/`.
   - Run targeted unit tests for shared icon-enabled primitives and domain icon mapping.
   - Run a smoke/e2e pass over representative services, artifacts, environments, deployments, and dialogs.

## Acceptance Criteria

- Planned service/artifact/environment/deployment surfaces render Tabler SVG icons instead of emoji for UI-provided icons.
- Touched routes import semantic icons from `web/src/lib/icons/domain-icons.js`, not raw package exports.
- `EmptyState`, `Table`, `Card`, `Modal`, and `ConfirmDialog` remain backward-compatible for callers that do not pass icon props.
- Blossom content-type rows use non-emoji icons with a generic fallback for unknown or missing MIME types.
- Existing navigation, row-click, modal open/close, and confirmation actions behave unchanged.
- Build and targeted tests pass.

## Risks and Mitigations

| Risk | Mitigation |
|---|---|
| Table changes break existing HTML-string renderers. | Preserve `render(row)` exactly; add separate `icon`/`text` column fields. |
| EmptyState migration becomes ambiguous. | Use explicit `iconComponent` and keep legacy string `icon` fallback. |
| Icon choices drift across routes. | Require route imports from `domain-icons.js`; keep raw Tabler imports out of route files. |
| Less-common Tabler export names differ from assumptions. | Validate exact names in `domain-icons.js` before route conversion. |
| Over-iconizing makes dense pages noisier. | Limit icons to entity identity, content type, major status, empty states, page headers, section headers, and dialog titles. |
| Accessibility regressions from decorative SVGs. | Mark decorative icons hidden and keep text labels as the accessible source of truth. |

## Open Questions

None blocking. Exact icon picks are implementation details inside `domain-icons.js`, with maritime-adjacent choices preferred only when they remain immediately understandable.

## References

- Tabler Icons Svelte docs: https://docs.tabler.io/icons/libraries/svelte
- Lucide Svelte docs: https://lucide.dev/guide/svelte
- Heroicons: https://heroicons.com/
- Material Symbols guide: https://developers.google.com/fonts/docs/material_symbols
