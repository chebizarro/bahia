# Verification report: bahia-dfc2

Date: 2026-06-07

## Evidence

- `npm run test:unit -- --run tests/unit/profile-publish.test.js tests/unit/nav.test.js tests/unit/settings-section-order.test.js tests/unit/auth-store.test.js`
  - Result: 4 files passed, 52 tests passed.
- `npm run lint`
  - Result: `svelte-check found 0 errors and 0 warnings`.

## Acceptance mapping

- AC1: `nav.test.js` verifies the UserMenu model links Edit Profile to `/settings/profile` and the route file exists.
- AC2: `profile-publish.test.js` verifies field validation and rejects invalid submissions before signing.
- AC3: `profile-publish.test.js` verifies kind-0 event construction uses `signWithAuth`; `auth-store.test.js` verifies profile cache persistence after successful publish.
- AC4: `profile-publish.test.js` verifies accepted and rejected relay OK outcomes are preserved and at least one accepted relay is required.
- AC5: `profile-publish.test.js` verifies all-relay rejection and failed writable relay connection behavior.
- AC6: `settings-section-order.test.js` verifies the Settings page links to the profile surface; `docs/user-guide/getting-started.md` documents profile publishing behavior.

## Scope notes

- No changes were made to the concurrent relay sidecar inventory documentation files.
- Standard Nostr kind `0` metadata was used; no Bahia-specific event kind definitions changed.
