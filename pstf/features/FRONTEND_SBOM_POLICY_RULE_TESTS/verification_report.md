# Verification Report: Frontend SBOM/Policy Rule Component Tests

Feature: `FRONTEND_SBOM_POLICY_RULE_TESTS`  
Source issue: `bahia-9v3y`

## Verified behavior

- `SBOMDetails.svelte` is covered for loading, empty, attestation details, NTIA compliance from attestation and SBOM fallback data, vulnerability count badges, package table rendering, missing package fields, and sparse SBOM objects.
- `PolicyRuleBuilder.svelte` is covered for empty and existing rule rendering, disabled controls, modal category/rule navigation, no-param rule creation, multiselect params, numeric defaults and overrides, select defaults and changes, text-list params, rule removal, and modal close behavior.
- `vitest.config.js` now includes `tests/unit/**/*.test.{js,ts}` so the new TypeScript specs are discovered alongside existing JavaScript specs.

## Test evidence

- `cd web && npx vitest run --config vitest.config.js tests/unit/SBOMDetails.test.ts tests/unit/PolicyRuleBuilder.test.ts` — passed, 17 tests.
- `cd web && npm test` — failed in unrelated existing specs: `auth-store.test.js`, `encrypted-controlplane.test.js`, `notifications-store.test.js`, and `repositories-store.test.js`. Follow-up issue filed: `bahia-whlg`.
- `cd web && npx vitest run --config vitest.config.js tests/unit/auth-store.test.js` — failed independently with the same auth-store timeouts, confirming the representative failure is not caused by the new component specs.
- `cd web && npm run build` — passed; emitted pre-existing Svelte warnings in route files unrelated to this change.

## Notes

`PolicyRuleBuilder.svelte` currently uses Svelte 5 bindable `rules` state; it does not dispatch a `createRule` event, and the JSON editor toggle is implemented in `web/src/routes/policies/+page.svelte`, not inside the component. Tests cover the component's current public behavior.
